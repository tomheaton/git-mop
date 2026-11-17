// git-mop finds every git repository under a folder and reports which worktrees and branches
// can be removed without losing work, and which need attention. git-mop apply removes what the
// last report offered, sync fast-forwards branches that are only behind, and where finds a branch.
//
// Every command takes --json, which prints one JSON document on stdout, errors included.
// AGENTS.md describes the documents and their reason codes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const usage = `git-mop: find the git worktrees and branches you're done with, and remove them safely

usage:
  git-mop [flags]                 report what can go, and save it as the plan to apply
  git-mop apply [flags]           remove what the saved plan lists, re-checking each item
  git-mop sync [flags]            fetch, then fast-forward branches that are only behind
  git-mop where <branch> [flags]  list the worktrees that have a branch (or glob) checked out

every command takes --json for one JSON document on stdout, and -h for its flags
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
	case "", "scan":
		command = "scan"
		scanCmd(args)
	case "apply":
		command = "apply"
		applyCmd(args)
	case "sync":
		command = "sync"
		syncCmd(args)
	case "where":
		command = "where"
		whereCmd(args)
	case "help":
		fmt.Print(usage)
	default:
		command = cmd
		jsonMode = wantsJSON(args)
		exitWith(exitUsage, &cliError{"unknown_command", fmt.Sprintf("unknown command %q, run git-mop help", cmd)})
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(strings.TrimSpace("git-mop "+name), flag.ContinueOnError)
	// the flag package prints usage on any error; parse reports errors itself, as JSON when asked
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// wantsJSON spots --json before the flags are parsed, so even a bad flag gets a JSON error.
func wantsJSON(args []string) bool {
	for _, a := range args {
		switch a {
		case "-json", "--json", "-json=true", "--json=true":
			return true
		}
	}
	return false
}

// parse parses flags, and with interleaved set lets them follow positional arguments,
// as in "where fix/login --repo api". It returns the positional arguments.
func parse(fs *flag.FlagSet, args []string, interleaved bool) []string {
	jsonMode = wantsJSON(args)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Print(usage + "\nflags for " + fs.Name() + ":\n")
				fs.SetOutput(os.Stdout)
				fs.PrintDefaults()
				os.Exit(exitOK)
			}
			exitWith(exitUsage, &cliError{"usage", err.Error() + " (-h lists the flags)"})
		}
		args = fs.Args()
		if !interleaved || len(args) == 0 {
			return append(pos, args...)
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

// reposFlags are the flags every command that walks a folder shares.
func reposFlags(fs *flag.FlagSet) (root *string, depth *int) {
	return fs.String("root", filepath.Join(home, "Developer"), "folder to search for repositories"),
		fs.Int("depth", 4, "how many folders deep to look for repositories")
}

func findRepos(root string, depth int) []string {
	if _, err := os.Stat(root); err != nil {
		exitWith(exitError, &cliError{"root_not_found", fmt.Sprintf("can't read %s: pass --root with the folder that holds your repositories", root)})
	}
	gitDirs, err := discover(root, depth)
	if err != nil {
		fatal(err)
	}
	return gitDirs
}

func mustLoadKeep(path string) keepList {
	keep, err := loadKeep(path, home)
	if err != nil {
		exitWith(exitError, failure("keep_file_invalid", err))
	}
	return keep
}

func scanCmd(args []string) {
	fs := newFlags("")
	root, depth := reposFlags(fs)
	jobs := fs.Int("jobs", 12, "repositories to scan at once")
	noFetch := fs.Bool("no-fetch", false, "don't fetch remotes first (faster, may be stale)")
	noGitHub := fs.Bool("no-github", false, "don't look up PRs, judge branches by git alone")
	fs.Bool("json", false, "print the result as one JSON document (see AGENTS.md)")
	verbose := fs.Bool("v", false, "list every kept branch and worktree")
	noSizes := fs.Bool("no-sizes", false, "skip measuring the worktrees to be removed")
	keepPath := fs.String("keep", defaultKeep, "keep list file")
	planPath := fs.String("plan", defaultPlan, "where to save the plan that git-mop apply removes")
	parse(fs, args, false)

	keep := mustLoadKeep(*keepPath)
	gitDirs := findRepos(*root, *depth)
	progress("scanning %s...", plural(len(gitDirs), "repository"))
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
	created := time.Now()
	if err := savePlan(*planPath, *root, created, p); err != nil {
		exitWith(exitError, failure("plan_write_failed", err))
	}
	if !*noSizes {
		measure(p.worktrees, *jobs)
	}
	d := buildScanDoc(*root, *planPath, created, found, p)
	if jsonMode {
		printJSON(d)
		return
	}
	renderScan(os.Stdout, d, *verbose)
}

// applyDoc is what --json prints for apply.
type applyDoc struct {
	header
	Plan    planInfo     `json:"plan"`
	Root    string       `json:"root"`
	Summary applySummary `json:"summary"`
	Results []applied    `json:"results"`
	LogPath string       `json:"logPath"`
}

type applySummary struct {
	Removed int `json:"removed"` // worktrees
	Deleted int `json:"deleted"` // branches
	Pruned  int `json:"pruned"`  // stale worktree entries
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

func applyCmd(args []string) {
	fs := newFlags("apply")
	planPath := fs.String("plan", defaultPlan, "plan saved by the last scan")
	maxAge := fs.Duration("max-age", 24*time.Hour, "refuse a plan older than this (0 for no limit)")
	keepPath := fs.String("keep", defaultKeep, "keep list file, for its ignorable patterns")
	logPath := fs.String("log", defaultLog, "where to log what was removed")
	only := fs.String("only", "", "comma-separated item ids from the scan's --json: remove only these, and keep the plan for the rest")
	fs.Bool("json", false, "print the result as one JSON document (see AGENTS.md)")
	parse(fs, args, false)

	keep := mustLoadKeep(*keepPath)
	sp, p, err := loadPlan(*planPath, *maxAge)
	if err != nil {
		fatal(err)
	}
	if *only != "" {
		if p, err = p.only(strings.Split(*only, ",")); err != nil {
			fatal(err)
		}
	}
	res, err := applyPlan(p, *logPath, keep)
	if err != nil {
		exitWith(exitError, failure("log_write_failed", err))
	}
	// a plan is used once, so the next full apply needs a fresh scan and review.
	// --only leaves it for the items not yet applied, which were reviewed too.
	if *only == "" {
		os.Remove(*planPath)
	}
	d := applyDoc{header: okHeader(), Plan: planInfo{*planPath, sp.Created, sp.Created.Add(*maxAge)}, Root: sp.Root, Results: res, LogPath: *logPath}
	for _, a := range res {
		switch a.Outcome {
		case "removed":
			d.Summary.Removed++
		case "deleted":
			d.Summary.Deleted++
		case "pruned":
			d.Summary.Pruned += a.Pruned
		case "skipped":
			d.Summary.Skipped++
		case "failed":
			d.Summary.Failed++
		}
	}
	if jsonMode {
		printJSON(d)
		return
	}
	renderApply(os.Stdout, d)
}

func renderApply(w io.Writer, d applyDoc) {
	s := outStyle
	fmt.Fprintf(w, "%s  %s\n\n", s.bold("git-mop apply"), s.dim("plan from "+d.Plan.CreatedAt.Format("Mon 2 Jan 15:04")+", scan of "+tildify(d.Root)))
	if len(d.Results) == 0 {
		fmt.Fprintln(w, s.dim("  The plan had nothing to remove."))
		return
	}
	var t table
	for _, a := range d.Results {
		mark, paint := "✓", s.green
		switch a.Outcome {
		case "skipped":
			mark, paint = "–", s.yellow
		case "failed":
			mark, paint = "✗", s.red
		}
		what, detail := a.Branch, ""
		switch a.Kind {
		case "worktree":
			what = relTo(d.Root, a.Path)
		case "stale_worktree":
			what = a.Repo
			if a.Outcome == "pruned" {
				detail = plural(a.Pruned, "stale entry")
			}
		}
		if a.Reason != nil {
			detail = a.Reason.Text
		}
		kind := a.Kind
		if kind == "stale_worktree" {
			kind = "stale"
		}
		repo := a.Repo
		if a.Kind == "stale_worktree" {
			repo = ""
		}
		t.add(c(mark, paint), c(a.Outcome, paint), c(kind, s.dim), c(repo, s.cyan), c(what, s.plain), c(detail, s.dim))
	}
	t.render(w, "  ")
	sum := d.Summary
	fmt.Fprintf(w, "\n  %s    %s    %s    %s    %s\n  %s\n",
		paintIf(sum.Removed > 0, s.green, s.dim)(plural(sum.Removed, "worktree")+" removed"),
		paintIf(sum.Deleted > 0, s.green, s.dim)(plural(sum.Deleted, "branch")+" deleted"),
		paintIf(sum.Pruned > 0, s.green, s.dim)(plural(sum.Pruned, "stale entry")+" pruned"),
		paintIf(sum.Skipped > 0, s.yellow, s.dim)(fmt.Sprintf("%d skipped", sum.Skipped)),
		paintIf(sum.Failed > 0, s.red, s.dim)(fmt.Sprintf("%d failed", sum.Failed)),
		s.dim("Every removal is logged with its SHA in "+tildify(d.LogPath)+": git branch <name> <sha> brings a branch back."))
}

func syncCmd(args []string) {
	fs := newFlags("sync")
	root, depth := reposFlags(fs)
	jobs := fs.Int("jobs", 12, "repositories to sync at once")
	noFetch := fs.Bool("no-fetch", false, "fast-forward to the remote branches as last fetched")
	dryRun := fs.Bool("dry-run", false, "fetch and report what would move, without moving anything")
	includeOpen := fs.Bool("include-open", false, "also update worktrees a process has open, such as a dev server")
	fs.Bool("json", false, "print the result as one JSON document (see AGENTS.md)")
	parse(fs, args, false)

	gitDirs := findRepos(*root, *depth)
	progress("syncing %s...", plural(len(gitDirs), "repository"))
	cwds := processCwds()
	reports := make([]syncReport, len(gitDirs))
	results := make([][]synced, len(gitDirs))
	forEach(len(gitDirs), *jobs, func(i int) {
		results[i] = syncRepo(gitDirs[i], !*noFetch, *includeOpen, *dryRun, cwds, &reports[i])
	})
	rep := syncReport{header: okHeader(), DryRun: *dryRun, Results: []synced{}, FetchFailed: []fetchFailure{}}
	rep.Summary.Repos = len(gitDirs)
	for i := range gitDirs {
		rep.Results = append(rep.Results, results[i]...)
		rep.FetchFailed = append(rep.FetchFailed, reports[i].FetchFailed...)
		rep.Summary.UpToDate += reports[i].Summary.UpToDate
		rep.Summary.NoUpstream += reports[i].Summary.NoUpstream
		rep.Summary.OtherUpstream += reports[i].Summary.OtherUpstream
		rep.Summary.DetachedWTs += reports[i].Summary.DetachedWTs
	}
	for _, s := range rep.Results {
		if s.Action == "skipped" {
			rep.Summary.Skipped++
		} else {
			rep.Summary.Updated++
		}
	}
	rep.Summary.FetchFailed = len(rep.FetchFailed)
	if jsonMode {
		printJSON(rep)
		return
	}
	renderSync(os.Stdout, rep)
}

// whereDoc is what --json prints for where.
type whereDoc struct {
	header
	Pattern string  `json:"pattern"`
	Matches []found `json:"matches"`
}

func whereCmd(args []string) {
	fs := newFlags("where")
	root, depth := reposFlags(fs)
	repo := fs.String("repo", "", "only look in the repository with this name, such as api")
	pathOnly := fs.Bool("path", false, "print only worktree paths, one per line, for cd $(git-mop where ...)")
	fs.Bool("json", false, "print the result as one JSON document (see AGENTS.md)")
	pos := parse(fs, args, true)
	if len(pos) != 1 {
		exitWith(exitUsage, &cliError{"usage", "where takes one branch name or glob, such as git-mop where 'fix/*'"})
	}
	res := where(findRepos(*root, *depth), pos[0], *repo)
	inWorktree := 0
	for _, f := range res {
		if f.Path != "" {
			inWorktree++
		}
	}
	switch {
	case jsonMode:
		printJSON(whereDoc{okHeader(), pos[0], append([]found{}, res...)})
	case *pathOnly:
		for _, f := range res {
			if f.Path != "" {
				fmt.Println(f.Path)
			}
		}
	default:
		renderWhere(os.Stdout, pos[0], res)
	}
	if inWorktree == 0 {
		os.Exit(exitNotFound)
	}
}

func renderWhere(w io.Writer, pattern string, res []found) {
	s := outStyle
	if len(res) == 0 {
		fmt.Fprintf(os.Stderr, "%s\n", errStyle.dim("no branch matches "+pattern))
		return
	}
	var t table
	for _, f := range res {
		mark, paint, loc := "●", s.green, tildify(f.Path)
		switch f.State {
		case "not_checked_out":
			mark, paint, loc = "○", s.dim, ""
		case "uncommitted_changes", "status_unreadable":
			paint = s.yellow
		}
		state := f.StateText
		if f.Main {
			state += ", main checkout"
		}
		t.add(c(mark, paint), c(f.Repo, s.cyan), c(f.Branch, s.bold), c(state, paint), c(loc, s.dim))
	}
	t.render(w, "")
}

// forEach runs fn for 0..n-1 with at most jobs running at once.
func forEach(n, jobs int, fn func(i int)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(jobs, 1))
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
