// git-mop finds every git repository under a folder and reports which worktrees and branches
// can be removed without losing work, and which need attention. git-mop apply removes what the
// last report offered, sync fast-forwards branches that are only behind, and where finds a branch.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const usage = `usage:
  git-mop [flags]                 report what can go, and save it as the plan to apply
  git-mop apply [flags]           remove what the saved plan lists, re-checking each item
  git-mop sync [flags]            fetch, then fast-forward branches that are only behind
  git-mop where <branch> [flags]  list the worktrees that have a branch (or glob) checked out

run any command with -h for its flags
`

var home, _ = os.UserHomeDir()

var (
	stateDir    = filepath.Join(home, ".local", "state", "git-mop")
	defaultKeep = filepath.Join(home, ".config", "git-mop", "keep")
	defaultPlan = filepath.Join(stateDir, "plan.json")
	defaultLog  = filepath.Join(stateDir, "removed.log")
)

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "":
		scanCmd(args)
	case "apply":
		applyCmd(args)
	case "sync":
		syncCmd(args)
	case "where":
		whereCmd(args)
	case "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "git-mop: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("git-mop "+name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage+"\nflags for "+fs.Name()+":\n")
		fs.PrintDefaults()
	}
	return fs
}

// reposFlags are the flags every command that walks a folder shares.
func reposFlags(fs *flag.FlagSet) (root *string, depth *int) {
	return fs.String("root", filepath.Join(home, "Developer"), "folder to search for repositories"),
		fs.Int("depth", 4, "how many folders deep to look for repositories")
}

// parseInterleaved lets flags come after positional arguments, as in "where fix/login --repo api".
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

func scanCmd(args []string) {
	fs := newFlags("")
	root, depth := reposFlags(fs)
	jobs := fs.Int("jobs", 12, "repositories to scan at once")
	noFetch := fs.Bool("no-fetch", false, "don't fetch remotes first (faster, may be stale)")
	noGitHub := fs.Bool("no-github", false, "don't look up PRs, judge branches by git alone")
	asJSON := fs.Bool("json", false, "print the full scan as JSON")
	verbose := fs.Bool("v", false, "list every kept branch and worktree")
	noSizes := fs.Bool("no-sizes", false, "skip measuring the worktrees to be removed")
	keepPath := fs.String("keep", defaultKeep, "keep list file")
	planPath := fs.String("plan", defaultPlan, "where to save the plan that git-mop apply removes")
	apply := fs.Bool("apply", false, "removed: run git-mop apply after reviewing the report")
	fs.Parse(args)
	if *apply {
		fatal(fmt.Errorf("--apply is now a command: review the report, then run git-mop apply"))
	}

	keep, err := loadKeep(*keepPath, home)
	if err != nil {
		fatal(err)
	}
	gitDirs, err := discover(*root, *depth)
	if err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "scanning %d repositories...\n", len(gitDirs))
	opt := scanOptions{fetch: !*noFetch, cwds: processCwds(), keep: keep}
	repos := make([]*Repo, len(gitDirs))
	forEach(len(gitDirs), *jobs, func(i int) {
		r := scanRepo(gitDirs[i], opt)
		if r == nil {
			return
		}
		if !*noGitHub {
			enrichWithPRs(r, keep)
		}
		classify(r, keep)
		repos[i] = r
	})
	var found []*Repo
	for _, r := range repos {
		if r != nil {
			found = append(found, r)
		}
	}
	keepNested(found)
	p := buildPlan(found)
	if err := savePlan(*planPath, *root, p); err != nil {
		fatal(err)
	}

	if *asJSON {
		printJSON(found)
		return
	}
	if !*noSizes {
		measure(p.worktrees, *jobs)
	}
	printReport(os.Stdout, *root, found, p, *verbose)
	if len(p.worktrees)+len(p.branches)+len(p.prune) > 0 {
		fmt.Printf("\nnothing removed: run git-mop apply to remove the items under SAFE TO REMOVE\nplan: %s\n", *planPath)
	}
}

func applyCmd(args []string) {
	fs := newFlags("apply")
	planPath := fs.String("plan", defaultPlan, "plan saved by the last scan")
	maxAge := fs.Duration("max-age", 24*time.Hour, "refuse a plan older than this (0 for no limit)")
	keepPath := fs.String("keep", defaultKeep, "keep list file, for its ignorable patterns")
	logPath := fs.String("log", defaultLog, "where to log what was removed")
	asJSON := fs.Bool("json", false, "print what was removed and skipped as JSON")
	fs.Parse(args)

	keep, err := loadKeep(*keepPath, home)
	if err != nil {
		fatal(err)
	}
	sp, p, err := loadPlan(*planPath, *maxAge)
	if err != nil {
		fatal(err)
	}
	var out io.Writer = os.Stdout
	if *asJSON {
		out = io.Discard
	} else {
		fmt.Printf("applying the plan from %s (scan of %s)\n\n", sp.Created.Format("2 Jan 15:04"), sp.Root)
	}
	res, err := applyPlan(out, p, *logPath, keep)
	if err != nil {
		fatal(err)
	}
	// a plan is used once: the next apply needs a fresh scan and review
	os.Remove(*planPath)
	if *asJSON {
		printJSON(res)
	}
}

func syncCmd(args []string) {
	fs := newFlags("sync")
	root, depth := reposFlags(fs)
	jobs := fs.Int("jobs", 12, "repositories to sync at once")
	noFetch := fs.Bool("no-fetch", false, "fast-forward to the remote branches as last fetched")
	dryRun := fs.Bool("dry-run", false, "fetch and report what would move, without moving anything")
	includeOpen := fs.Bool("include-open", false, "also update worktrees a process has open, such as a dev server")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	fs.Parse(args)

	gitDirs, err := discover(*root, *depth)
	if err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "syncing %d repositories...\n", len(gitDirs))
	cwds := processCwds()
	reports := make([]syncReport, len(gitDirs))
	results := make([][]synced, len(gitDirs))
	forEach(len(gitDirs), *jobs, func(i int) {
		results[i] = syncRepo(gitDirs[i], !*noFetch, *includeOpen, *dryRun, cwds, &reports[i])
	})
	rep := syncReport{Repos: len(gitDirs), DryRun: *dryRun, Results: []synced{}, FetchFailed: []string{}}
	for i := range gitDirs {
		rep.Results = append(rep.Results, results[i]...)
		rep.FetchFailed = append(rep.FetchFailed, reports[i].FetchFailed...)
		rep.UpToDate += reports[i].UpToDate
		rep.NoUpstream += reports[i].NoUpstream
		rep.OtherUpstream += reports[i].OtherUpstream
		rep.DetachedWTs += reports[i].DetachedWTs
	}
	if *asJSON {
		printJSON(rep)
		return
	}
	printSync(os.Stdout, rep)
}

func whereCmd(args []string) {
	fs := newFlags("where")
	root, depth := reposFlags(fs)
	repo := fs.String("repo", "", "only look in the repository with this name, such as api")
	pathOnly := fs.Bool("path", false, "print only worktree paths, one per line, for cd $(git-mop where ...)")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		fs.Usage()
		os.Exit(2)
	}
	gitDirs, err := discover(*root, *depth)
	if err != nil {
		fatal(err)
	}
	res := where(gitDirs, pos[0], *repo)
	inWorktree := 0
	for _, f := range res {
		if f.Path != "" {
			inWorktree++
		}
	}
	switch {
	case *asJSON:
		if res == nil {
			res = []found{}
		}
		printJSON(res)
	case *pathOnly:
		for _, f := range res {
			if f.Path != "" {
				fmt.Println(f.Path)
			}
		}
	default:
		printWhere(res)
	}
	if inWorktree == 0 {
		os.Exit(1)
	}
}

func printWhere(res []found) {
	if len(res) == 0 {
		fmt.Fprintln(os.Stderr, "no branch matches")
		return
	}
	for _, f := range res {
		state := "clean"
		switch {
		case f.Path == "":
			fmt.Printf("%s\t%s\tnot checked out\n", f.Repo, f.Branch)
			continue
		case f.Dirty < 0:
			state = "status unreadable"
		case f.Dirty > 0:
			state = plural(f.Dirty, "uncommitted change")
		}
		if f.Main {
			state += ", main checkout"
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", f.Repo, f.Branch, f.Path, state)
	}
}

// forEach runs fn for 0..n-1 with at most jobs running at once.
func forEach(n, jobs int, fn func(i int)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fatal(err)
	}
}

// loadKeep reads the keep list: "<repo name> <branch>" per line, "path <folder>" to keep a worktree
// or everything under a folder, "throwaway <repo name> <glob>" for branches whose local-only
// commits don't need keeping, "ignorable <glob>" for ignored files that are fine to lose with
// a worktree, or "longlived <branch>" for a branch that PRs merge into and that is never deleted.
// Lines starting with # are comments.
func loadKeep(path, home string) (keepList, error) {
	k := keepList{branches: map[string]bool{}, longLived: map[string]bool{}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return k, nil
	}
	if err != nil {
		return k, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) == 3 && fields[0] == "throwaway" {
			k.throwaway = append(k.throwaway, fields[1]+" "+fields[2])
			continue
		}
		if len(fields) != 2 {
			return k, fmt.Errorf("%s: want \"<repo> <branch>\", \"path <folder>\", \"throwaway <repo> <glob>\", \"ignorable <glob>\" or \"longlived <branch>\", got %q", path, l)
		}
		if fields[0] == "ignorable" {
			k.ignorable = append(k.ignorable, fields[1])
			continue
		}
		if fields[0] == "longlived" {
			k.longLived[fields[1]] = true
			continue
		}
		if fields[0] == "path" {
			p := fields[1]
			if strings.HasPrefix(p, "~/") {
				p = filepath.Join(home, p[2:])
			}
			k.paths = append(k.paths, filepath.Clean(p))
			continue
		}
		k.branches[fields[0]+" "+fields[1]] = true
	}
	return k, sc.Err()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "git-mop:", err)
	os.Exit(1)
}
