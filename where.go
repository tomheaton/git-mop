package main

import (
	"path"
	"strings"
)

// found is one place a branch lives: a worktree that has it checked out, or a repository where it exists without one.
type found struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path,omitempty"` // empty when no worktree has it checked out
	Main   bool   `json:"main"`
	Dirty  int    `json:"dirty"`
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
				f := found{Repo: name, Branch: b, Path: cur, Main: curMain}
				if st, err := gitIn(cur, "status", "--porcelain"); err == nil {
					f.Dirty = len(lines(st))
				} else {
					f.Dirty = -1
				}
				res = append(res, f)
			}
		}
		refs, _ := git(g, "for-each-ref", "--format=%(refname:short)", "refs/heads")
		for _, b := range lines(refs) {
			if match(b) && !checkedOut[b] {
				res = append(res, found{Repo: name, Branch: b})
			}
		}
	}
	return res
}
