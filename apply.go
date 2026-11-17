package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type applyResult struct {
	Removed []string  `json:"removed"` // worktree paths
	Deleted []string  `json:"deleted"` // "<repo> <branch>"
	Pruned  int       `json:"pruned"`
	Skipped []skipped `json:"skipped"`
}

type skipped struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// applyPlan removes the planned items, re-checking each one against what the scan saw first.
// Anything that changed since the scan is skipped. Every removal is logged with its SHA.
func applyPlan(w io.Writer, p plan, logPath string, keep keepList) (applyResult, error) {
	res := applyResult{Removed: []string{}, Deleted: []string{}, Skipped: []skipped{}}
	skip := func(verb, target, reason string) {
		fmt.Fprintf(w, "%s %s: %s\n", verb, target, reason)
		res.Skipped = append(res.Skipped, skipped{target, reason})
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return res, err
	}
	logf, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return res, err
	}
	defer logf.Close()
	record := func(kind, gitDir, name, sha string) {
		fmt.Fprintf(logf, "%s\t%s\t%s\t%s\t%s\n", time.Now().Format(time.RFC3339), kind, gitDir, name, sha)
	}
	cwds := processCwds()

	// deepest first, so a worktree nested inside another goes before its parent
	wts := append([]item(nil), p.worktrees...)
	sort.SliceStable(wts, func(i, j int) bool { return len(wts[i].wt.Path) > len(wts[j].wt.Path) })
	for _, it := range wts {
		if why := worktreeChanged(it, cwds, keep); why != "" {
			skip("skip", it.wt.Path, why)
			continue
		}
		main := it.repo.Worktrees[0].Path
		if _, err := gitIn(main, "worktree", "remove", it.wt.Path); err != nil {
			// git refuses worktrees with submodules even when clean, which worktreeChanged has just checked
			if !strings.Contains(err.Error(), "submodule") {
				skip("fail", it.wt.Path, firstLine(err.Error()))
				continue
			}
			if _, err := gitIn(main, "worktree", "remove", "--force", it.wt.Path); err != nil {
				skip("fail", it.wt.Path, firstLine(err.Error()))
				continue
			}
		}
		record("worktree", it.repo.GitDir, it.wt.Path, it.wt.Head)
		res.Removed = append(res.Removed, it.wt.Path)
		if it.branch != nil {
			deleteBranch(it, record, skip, &res)
		}
	}
	for _, it := range p.branches {
		deleteBranch(it, record, skip, &res)
	}
	seen := map[string]bool{}
	for _, it := range p.prune {
		if seen[it.repo.GitDir] {
			continue
		}
		seen[it.repo.GitDir] = true
		before := countPrunable(it.repo.GitDir)
		if _, err := git(it.repo.GitDir, "worktree", "prune"); err != nil {
			skip("fail", "prune "+repoName(it.repo), firstLine(err.Error()))
			continue
		}
		res.Pruned += before - countPrunable(it.repo.GitDir)
	}
	fmt.Fprintf(w, "\nremoved %s, deleted %s, pruned %s, skipped %d\nlog: %s\n",
		plural(len(res.Removed), "worktree"), plural(len(res.Deleted), "branch"), plural(res.Pruned, "stale entry"), len(res.Skipped), logPath)
	return res, nil
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

// worktreeChanged returns why a worktree no longer matches what the scan judged safe, or "" if it still does.
func worktreeChanged(it item, cwds map[string][]string, keep keepList) string {
	w := it.wt
	if _, err := os.Stat(w.Path); err != nil {
		return "folder is gone"
	}
	head, err := gitIn(w.Path, "rev-parse", "HEAD")
	if err != nil || head != w.Head {
		return "HEAD moved since the scan"
	}
	cur, _ := gitIn(w.Path, "branch", "--show-current")
	if cur != w.Branch {
		return "now on " + cur
	}
	if st, err := gitIn(w.Path, "status", "--porcelain"); err != nil || st != "" {
		return "has uncommitted changes"
	}
	if users := usersOf(w.Path, cwds); len(users) > 0 {
		return "open in " + strings.Join(users, ", ")
	}
	if ig := valuableIgnored(w.Path, keep); len(ig) > 0 {
		return "has ignored files removing it would delete: " + describeIgnored(ig)
	}
	return ""
}

func deleteBranch(it item, record func(kind, gitDir, name, sha string), skip func(verb, target, reason string), res *applyResult) {
	b, r := it.branch, it.repo
	target := repoName(r) + " " + b.Name
	tip, err := git(r.GitDir, "rev-parse", "-q", "--verify", "refs/heads/"+b.Name)
	if err != nil {
		skip("skip", target, "branch is gone")
		return
	}
	if tip != b.Tip {
		skip("skip", target, "new commits since the scan")
		return
	}
	out, _ := git(r.GitDir, "worktree", "list", "--porcelain")
	for _, l := range lines(out) {
		if l == "branch refs/heads/"+b.Name {
			skip("skip", target, "checked out in a worktree")
			return
		}
	}
	if _, err := git(r.GitDir, "branch", "-D", b.Name); err != nil {
		skip("fail", target, firstLine(err.Error()))
		return
	}
	record("branch", r.GitDir, b.Name, tip)
	res.Deleted = append(res.Deleted, target)
}
