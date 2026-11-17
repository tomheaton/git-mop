package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var githubURL = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(\.git)?/?$`)

// defaultLongLived are branches that PRs merge into that aren't the default branch but are never deleted.
// The keep list adds more with "longlived <branch>".
var defaultLongLived = map[string]bool{"main": true, "master": true, "develop": true, "staging": true, "production": true}

type scanOptions struct {
	fetch bool
	keep  keepList
	cwds  map[string][]string // process working dirs to the commands running in them
}

func scanRepo(gitDir string, opt scanOptions) *Repo {
	r := &Repo{GitDir: gitDir}
	url, err := git(gitDir, "remote", "get-url", "origin")
	if err != nil {
		return nil // no origin: nothing to compare against
	}
	if m := githubURL.FindStringSubmatch(url); m != nil {
		r.Slug = m[1] + "/" + m[2]
	}
	if opt.fetch {
		if _, err := git(gitDir, "fetch", "--prune", "--quiet", "origin"); err != nil {
			msg := firstLine(err.Error())
			r.FetchError = msg[strings.LastIndex(msg, ": ")+2:]
		}
	}
	r.Default, _ = git(gitDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if r.Default == "" {
		for _, b := range []string{"origin/main", "origin/master"} {
			if gitOK(gitDir, "rev-parse", "-q", "--verify", b) {
				r.Default = b
				break
			}
		}
	}
	if r.Default == "" {
		return nil
	}
	r.DefaultName = strings.TrimPrefix(r.Default, "origin/")
	if out, _ := git(gitDir, "stash", "list"); out != "" {
		r.Stashes = len(lines(out))
	}
	if gitOK(gitDir, "rev-parse", "-q", "--verify", "refs/heads/"+r.DefaultName) {
		r.DefaultAhead = gitCount(gitDir, r.Default+".."+"refs/heads/"+r.DefaultName)
	}
	r.Worktrees = scanWorktrees(r, opt)
	r.Branches = scanBranches(r)
	return r
}

func scanWorktrees(r *Repo, opt scanOptions) []*Worktree {
	out, _ := git(r.GitDir, "worktree", "list", "--porcelain")
	var wts []*Worktree
	var cur *Worktree
	for _, l := range append(lines(out), "") {
		switch {
		case strings.HasPrefix(l, "worktree "):
			cur = &Worktree{Path: strings.TrimPrefix(l, "worktree "), Main: len(wts) == 0}
		case strings.HasPrefix(l, "HEAD ") && cur != nil:
			cur.Head = strings.TrimPrefix(l, "HEAD ")
		case strings.HasPrefix(l, "branch ") && cur != nil:
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(l, "branch "), "refs/heads/")
		case strings.HasPrefix(l, "prunable") && cur != nil:
			cur.Prunable = true
		case strings.HasPrefix(l, "locked") && cur != nil:
			cur.Locked = true
		case l == "" && cur != nil:
			wts = append(wts, cur)
			cur = nil
		}
	}
	for _, w := range wts {
		if w.Prunable {
			continue
		}
		if _, err := os.Stat(w.Path); err != nil {
			w.Prunable = true
			continue
		}
		if st, err := gitIn(w.Path, "status", "--porcelain"); err == nil {
			w.Dirty = len(lines(st))
		} else {
			w.Dirty = -1 // unreadable counts as dirty
		}
		w.InUse = usersOf(w.Path, opt.cwds)
		if !w.Main {
			w.Ignored = valuableIgnored(w.Path, opt.keep)
		}
		if w.Branch == "" && w.Head != "" {
			w.HeadInDefault = gitOK(r.GitDir, "merge-base", "--is-ancestor", w.Head, r.Default)
			on, _ := git(r.GitDir, "branch", "-r", "--contains", w.Head)
			w.HeadOnRemote = on != ""
		}
	}
	return wts
}

func scanBranches(r *Repo) []*Branch {
	out, _ := git(r.GitDir, "for-each-ref", "--format=%(refname:short)%09%(objectname)%09%(upstream:short)%09%(upstream:track)%09%(committerdate:unix)", "refs/heads")
	byBranch := map[string]*Worktree{}
	for _, w := range r.Worktrees {
		if w.Branch != "" {
			byBranch[w.Branch] = w
		}
	}
	var bs []*Branch
	for _, l := range lines(out) {
		f := strings.Split(l, "\t")
		if len(f) < 5 || f[0] == r.DefaultName {
			continue
		}
		b := &Branch{Name: f[0], Tip: f[1], Upstream: f[2], UpstreamGone: f[3] == "[gone]", BeyondPR: -1}
		if ts, err := strconv.ParseInt(f[4], 10, 64); err == nil {
			b.LastCommit = time.Unix(ts, 0)
		}
		if w := byBranch[b.Name]; w != nil {
			b.Worktree, b.WorktreePath = w, w.Path
		}
		if b.Upstream != "" && !b.UpstreamGone {
			b.Ahead = gitCount(r.GitDir, b.Upstream+".."+b.Tip)
		}
		b.NotOnRemote = gitCount(r.GitDir, b.Tip, "--not", "--remotes")
		b.InDefault = gitOK(r.GitDir, "merge-base", "--is-ancestor", b.Tip, r.Default)
		if !b.InDefault {
			b.SameContent = squashed(r.GitDir, r.Default, b.Tip)
		}
		bs = append(bs, b)
	}
	return bs
}

// squashed reports whether the branch's whole diff from its merge base already appears in the default branch,
// which is how a squash merge looks locally.
func squashed(gitDir, def, tip string) bool {
	mb, err := git(gitDir, "merge-base", def, tip)
	if err != nil || mb == "" {
		return false
	}
	tree, err := git(gitDir, "rev-parse", tip+"^{tree}")
	if err != nil {
		return false
	}
	c, err := git(gitDir, "commit-tree", tree, "-p", mb, "-m", "_")
	if err != nil {
		return false
	}
	out, err := git(gitDir, "cherry", def, c)
	return err == nil && strings.HasPrefix(out, "-")
}

func usersOf(path string, cwds map[string][]string) []string {
	var users []string
	for dir, cmds := range cwds {
		if dir == path || strings.HasPrefix(dir, path+"/") {
			users = append(users, cmds...)
		}
	}
	return dedupe(users)
}

// processCwds maps every process's working directory to its command names, using lsof once.
// Without that list a worktree with a shell or editor open in it would look unused, so it stops
// rather than carry on. lsof exits non-zero when it can't read some processes, which is fine.
func processCwds() map[string][]string {
	if _, err := exec.LookPath("lsof"); err != nil {
		fatal(fmt.Errorf("lsof is needed to check that no process is working inside a worktree: %w", err))
	}
	out, err := run("lsof", "-a", "-d", "cwd", "-Fpcn")
	if out == "" {
		fatal(fmt.Errorf("lsof listed no processes, so it can't check which worktrees are in use: %v", err))
	}
	m := map[string][]string{}
	self := strconv.Itoa(os.Getpid())
	var pid, cmd string
	for _, l := range lines(out) {
		if l == "" {
			continue
		}
		switch l[0] {
		case 'p':
			pid, cmd = l[1:], ""
		case 'c':
			cmd = l[1:]
		case 'n':
			if pid != self {
				m[l[1:]] = append(m[l[1:]], cmd)
			}
		}
	}
	return m
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
