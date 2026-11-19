package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// applied is what apply did with one item of the plan.
type applied struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`    // worktree, branch or stale_worktree
	Outcome  string  `json:"outcome"` // removed, deleted, pruned, skipped or failed
	Repo     string  `json:"repo"`
	RepoPath string  `json:"repoPath"`
	Path     string  `json:"path,omitempty"`
	Branch   string  `json:"branch,omitempty"`
	Commit   string  `json:"commit,omitempty"` // what a removed worktree had checked out, or a deleted branch's tip
	Pruned   int     `json:"pruned,omitempty"` // stale entries pruned
	Reason   *Reason `json:"reason,omitempty"` // why it was skipped or failed
}

// applyPlan removes the planned items, re-checking each one against what the scan saw first.
// Anything that changed since the scan is skipped. Every removal is logged with its SHA.
func applyPlan(p plan, logPath string, keep keepList) ([]applied, error) {
	res := []applied{}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return res, err
	}
	logf, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return res, err
	}
	defer logf.Close()
	if err := logf.Chmod(0o600); err != nil { // a log from an older version was readable by everyone
		return res, err
	}
	record := func(kind, gitDir, name, sha string) {
		fmt.Fprintf(logf, "%s\t%s\t%s\t%s\t%s\n", time.Now().Format(time.RFC3339), kind, gitDir, name, sha)
	}
	cwds := processCwds()

	// deepest first, so a worktree nested inside another goes before its parent
	wts := append([]item(nil), p.worktrees...)
	sort.SliceStable(wts, func(i, j int) bool { return len(wts[i].wt.Path) > len(wts[j].wt.Path) })
	for _, it := range wts {
		a := applied{ID: worktreeID(it.wt.Path), Kind: "worktree", Repo: repoName(it.repo), RepoPath: repoPath(it.repo), Path: it.wt.Path, Branch: it.wt.Branch, Commit: it.wt.Head}
		if why := worktreeChanged(it, cwds, keep); why != nil {
			res = append(res, a.skipped(why))
			continue
		}
		main := repoPath(it.repo)
		if _, err := gitIn(main, "worktree", "remove", it.wt.Path); err != nil {
			// git refuses worktrees with submodules even when clean. Forcing deletes the submodules'
			// repositories, which worktreeChanged has just checked hold nothing that isn't on a remote.
			if !strings.Contains(err.Error(), "submodule") {
				res = append(res, a.failed(err))
				continue
			}
			if _, err := gitIn(main, "worktree", "remove", "--force", it.wt.Path); err != nil {
				res = append(res, a.failed(err))
				continue
			}
		}
		record("worktree", it.repo.GitDir, it.wt.Path, it.wt.Head)
		a.Outcome = "removed"
		res = append(res, a)
		if it.branch != nil {
			res = append(res, deleteBranch(it, record))
		}
	}
	for _, it := range p.branches {
		res = append(res, deleteBranch(it, record))
	}
	seen := map[string]bool{}
	for _, it := range p.prune {
		if seen[it.repo.GitDir] {
			continue
		}
		seen[it.repo.GitDir] = true
		a := applied{ID: pruneID(it.repo), Kind: "stale_worktree", Repo: repoName(it.repo), RepoPath: repoPath(it.repo)}
		before := countPrunable(it.repo.GitDir)
		if _, err := git(it.repo.GitDir, "worktree", "prune"); err != nil {
			res = append(res, a.failed(err))
			continue
		}
		a.Outcome, a.Pruned = "pruned", before-countPrunable(it.repo.GitDir)
		res = append(res, a)
	}
	return res, nil
}

func (a applied) skipped(why *Reason) applied {
	a.Outcome, a.Reason = "skipped", why
	return a
}

func (a applied) failed(err error) applied {
	msg := firstLine(err.Error())
	a.Outcome, a.Reason = "failed", &Reason{"git_failed", msg[strings.LastIndex(msg, ": ")+2:]}
	return a
}

func countPrunable(gitDir string) int {
	out, _ := git(gitDir, "worktree", "list", "--porcelain")
	n := 0
	for _, l := range lines(out) {
		if strings.HasPrefix(l, "prunable") {
			n++
		}
	}
	return n
}

// worktreeChanged returns why a worktree no longer matches what the scan judged safe, or nil if it still does.
func worktreeChanged(it item, cwds map[string][]string, keep keepList) *Reason {
	w := it.wt
	if _, err := os.Stat(w.Path); err != nil {
		return &Reason{"folder_gone", "folder is gone"}
	}
	head, err := gitIn(w.Path, "rev-parse", "HEAD")
	if err != nil || head != w.Head {
		return &Reason{"head_moved", "HEAD moved since the scan"}
	}
	cur, _ := gitIn(w.Path, "branch", "--show-current")
	if cur != w.Branch {
		return &Reason{"branch_changed", "now on " + cur}
	}
	if st, err := statusIn(w.Path); err != nil || st != "" {
		return &Reason{"uncommitted_changes", "has uncommitted changes"}
	}
	if users := usersOf(w.Path, cwds); len(users) > 0 {
		return &Reason{"in_use", "open in " + strings.Join(users, ", ")}
	}
	if ig := valuableIgnored(w.Path, keep); len(ig) > 0 {
		return &Reason{"ignored_files", "has ignored files removing it would delete: " + describeIgnored(ig)}
	}
	if work := submoduleWork(w.Path, keep); work != "" {
		return &Reason{"submodule_work", work}
	}
	return nil
}

func deleteBranch(it item, record func(kind, gitDir, name, sha string)) applied {
	b, r := it.branch, it.repo
	a := applied{ID: branchID(r, b.Name), Kind: "branch", Repo: repoName(r), RepoPath: repoPath(r), Branch: b.Name, Commit: b.Tip}
	tip, err := git(r.GitDir, "rev-parse", "-q", "--verify", "refs/heads/"+b.Name)
	if err != nil {
		return a.skipped(&Reason{"branch_gone", "branch is gone"})
	}
	if tip != b.Tip {
		return a.skipped(&Reason{"branch_moved", "new commits since the scan"})
	}
	// without the list there's no telling whether a worktree has the branch checked out, so it stays
	out, err := git(r.GitDir, "worktree", "list", "--porcelain")
	if err != nil {
		return a.failed(err)
	}
	for _, l := range lines(out) {
		if l == "branch refs/heads/"+b.Name {
			return a.skipped(&Reason{"checked_out", "checked out in a worktree"})
		}
	}
	// delete only if the branch still points at the tip the scan judged, so a commit made since the checks above survives
	if _, err := git(r.GitDir, "update-ref", "-m", "git-mop apply: delete", "-d", "refs/heads/"+b.Name, b.Tip); err != nil {
		return a.skipped(&Reason{"branch_moved", "branch moved: " + firstLine(err.Error())})
	}
	git(r.GitDir, "config", "--remove-section", "branch."+b.Name) // its upstream settings, as git branch -D does; most branches have none
	record("branch", r.GitDir, b.Name, tip)
	a.Outcome = "deleted"
	return a
}

// IDs name items across a scan and the apply that follows, for apply --only. They're opaque to
// callers but stay readable: a worktree is its path, a branch its repository and name (git
// forbids ":" in branch names), and pruning stale entries is per repository.
func worktreeID(path string) string        { return "worktree:" + path }
func pruneID(r *Repo) string               { return "prune:" + repoPath(r) }
func branchID(r *Repo, name string) string { return "branch:" + repoPath(r) + ":" + name }

// repoPath is the repository's main checkout.
func repoPath(r *Repo) string {
	if len(r.Worktrees) > 0 {
		return r.Worktrees[0].Path
	}
	return strings.TrimSuffix(r.GitDir, "/.git")
}

// only narrows a plan to the items with the given IDs, and fails on any ID it doesn't hold, so a
// typo never silently applies nothing.
func (p plan) only(ids []string) (plan, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out plan
	keep := func(items []item, id func(item) string) []item {
		var kept []item
		for _, it := range items {
			if want[id(it)] {
				kept = append(kept, it)
			}
		}
		for _, it := range kept {
			delete(want, id(it))
		}
		return kept
	}
	out.worktrees = keep(p.worktrees, func(it item) string { return worktreeID(it.wt.Path) })
	out.branches = keep(p.branches, func(it item) string { return branchID(it.repo, it.branch.Name) })
	out.prune = keep(p.prune, func(it item) string { return pruneID(it.repo) })
	if len(want) > 0 {
		var missing []string
		for id := range want {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return out, &cliError{"unknown_id", "not in the saved plan: " + strings.Join(missing, ", ") + " (run git-mop --json to see the plan's IDs)"}
	}
	return out, nil
}
