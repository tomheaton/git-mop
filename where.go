package main

import (
	"path"
	"strings"
)

// found is one place a branch lives: a worktree that has it checked out, or a repository where it exists without one.
type found struct {
	Repo      string `json:"repo"`
	RepoPath  string `json:"repoPath"`
	Branch    string `json:"branch"`
	Path      string `json:"path,omitempty"` // empty when no worktree has it checked out
	Main      bool   `json:"main"`           // the repository's main checkout
	State     string `json:"state"`          // clean, uncommitted_changes, status_unreadable or not_checked_out
	StateText string `json:"stateText"`
	Dirty     int    `json:"uncommittedChanges"`
}

// where finds a branch, or a glob of branches, across repositories. Repo filters by repository name.
func where(gitDirs []string, pattern, repo string) []found {
	match := func(b string) bool {
		if ok, _ := path.Match(pattern, b); ok {
			return true
		}
		return b == pattern
	}
	var res []found
	for _, g := range gitDirs {
		r := &Repo{GitDir: g}
		if url, err := git(g, "remote", "get-url", "origin"); err == nil {
			if m := githubURL.FindStringSubmatch(url); m != nil {
				r.Slug = m[1] + "/" + m[2]
			}
		}
		name := repoName(r)
		if repo != "" && name != repo {
			continue
		}
		checkedOut := map[string]bool{}
		out, _ := git(g, "worktree", "list", "--porcelain")
		var cur string
		first := true
		var curMain bool
		for _, l := range lines(out) {
			switch {
			case strings.HasPrefix(l, "worktree "):
				cur, curMain, first = strings.TrimPrefix(l, "worktree "), first, false
			case strings.HasPrefix(l, "branch "):
				b := strings.TrimPrefix(l, "branch refs/heads/")
				if !match(b) {
					continue
				}
				checkedOut[b] = true
				f := found{Repo: name, RepoPath: strings.TrimSuffix(g, "/.git"), Branch: b, Path: cur, Main: curMain, State: "clean", StateText: "clean"}
				if st, err := statusIn(cur); err != nil {
					f.State, f.StateText = "status_unreadable", "status unreadable"
				} else if f.Dirty = len(lines(st)); f.Dirty > 0 {
					f.State, f.StateText = "uncommitted_changes", plural(f.Dirty, "uncommitted change")
				}
				res = append(res, f)
			}
		}
		refs, _ := git(g, "for-each-ref", "--format=%(refname:short)", "refs/heads")
		for _, b := range lines(refs) {
			if match(b) && !checkedOut[b] {
				res = append(res, found{Repo: name, RepoPath: strings.TrimSuffix(g, "/.git"), Branch: b, State: "not_checked_out", StateText: "not checked out"})
			}
		}
	}
	return res
}
