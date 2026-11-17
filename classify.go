package main

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// keepList holds branches and paths the user wants left alone whatever their state,
// and branch patterns whose local-only commits don't matter, such as release cuts that are never pushed.
type keepList struct {
	branches  map[string]bool // "<repo name> <branch>"
	paths     []string
	throwaway []string        // "<repo name> <glob>"
	ignorable []string        // globs for ignored files that are fine to lose with a worktree
	longLived map[string]bool // branch names, in any repo, that are never deleted
}

// isLongLived reports whether a branch is one that PRs merge into and that never goes away.
func (k keepList) isLongLived(b string) bool {
	return defaultLongLived[b] || k.longLived[b]
}

func (k keepList) disposable(r *Repo, b string) (string, bool) {
	for _, t := range k.throwaway {
		repo, glob, _ := strings.Cut(t, " ")
		if ok, _ := path.Match(glob, b); ok && repo == repoName(r) {
			return glob, true
		}
	}
	return "", false
}

func (k keepList) branch(r *Repo, b string) bool {
	return k.branches[repoName(r)+" "+b]
}

func (k keepList) path(p string) bool {
	if p == "" {
		return false
	}
	for _, kp := range k.paths {
		if p == kp || strings.HasPrefix(p, kp+"/") {
			return true
		}
	}
	return false
}

func repoName(r *Repo) string {
	if r.Slug != "" {
		return r.Slug[strings.LastIndex(r.Slug, "/")+1:]
	}
	return filepath.Base(strings.TrimSuffix(r.GitDir, "/.git"))
}

// classifyBranch decides what can happen to a local branch. Safe means deleting it loses no commit:
// everything on it is in the default branch, or GitHub keeps it as a merged or closed PR's head.
func classifyBranch(r *Repo, b *Branch, keep keepList) Verdict {
	w := b.Worktree
	if keep.branch(r, b.Name) || (w != nil && keep.path(w.Path)) {
		return Verdict{Keep, "in your keep list"}
	}
	if keep.isLongLived(b.Name) {
		return Verdict{Keep, "long-lived branch"}
	}
	if b.PR != nil && b.PR.State == "OPEN" {
		return Verdict{Keep, fmt.Sprintf("open PR #%d", b.PR.Number)}
	}
	done, why := branchDone(r, b)
	if glob, ok := keep.disposable(r, b.Name); ok && !done {
		done, why = true, "throwaway branch ("+glob+" in your keep file), "+why
	}
	if w != nil {
		switch {
		case w.Main && done:
			return Verdict{Review, "main checkout is on a finished branch (" + why + "), switch it to " + r.DefaultName + " first"}
		case w.Main:
			return Verdict{Keep, "main checkout"}
		case w.Dirty != 0:
			return Verdict{Keep, fmt.Sprintf("worktree has %s", plural(w.Dirty, "uncommitted change"))}
		case len(w.InUse) > 0:
			return Verdict{Keep, "worktree is open in " + strings.Join(w.InUse, ", ")}
		case done && len(w.Ignored) > 0:
			return Verdict{Review, why + ", but removing the worktree deletes ignored " + describeIgnored(w.Ignored)}
		}
	}
	if done {
		return Verdict{Safe, why}
	}
	return Verdict{Keep, why}
}

// branchDone reports whether everything on the branch already lives somewhere that outlasts it, and why.
func branchDone(r *Repo, b *Branch) (bool, string) {
	if p := b.PR; p != nil {
		switch {
		case p.State == "MERGED" && b.BeyondPR < 0:
			return false, fmt.Sprintf("merged PR #%d, but its head couldn't be compared", p.Number)
		case p.State == "MERGED" && b.BeyondPR > 0:
			return false, fmt.Sprintf("%s beyond merged PR #%d", plural(b.BeyondPR, "commit"), p.Number)
		case p.State == "MERGED" && b.Stack != "":
			return false, b.Stack
		case p.State == "MERGED":
			return true, fmt.Sprintf("merged PR #%d", p.Number)
		case p.State == "CLOSED" && b.BeyondPR == 0:
			return true, fmt.Sprintf("closed PR #%d, GitHub keeps its commits", p.Number)
		case p.State == "CLOSED":
			return false, fmt.Sprintf("closed PR #%d, with %s not in it", p.Number, plural(b.BeyondPR, "commit"))
		}
	}
	switch {
	case b.InDefault:
		return true, "already in " + r.DefaultName
	case b.NotOnRemote > 0:
		return false, fmt.Sprintf("no PR, %s only on this machine", plural(b.NotOnRemote, "commit"))
	case b.SameContent:
		return false, "no PR, its changes look squash-merged into " + r.DefaultName
	default:
		return false, "no PR, pushed but not merged"
	}
}

// classifyDetached decides what can happen to a linked worktree with no branch checked out.
func classifyDetached(r *Repo, w *Worktree, keep keepList) Verdict {
	switch {
	case keep.path(w.Path):
		return Verdict{Keep, "in your keep list"}
	case w.Dirty != 0:
		return Verdict{Keep, fmt.Sprintf("%s", plural(w.Dirty, "uncommitted change"))}
	case len(w.InUse) > 0:
		return Verdict{Keep, "open in " + strings.Join(w.InUse, ", ")}
	case (w.HeadInDefault || w.HeadOnRemote) && len(w.Ignored) > 0:
		return Verdict{Review, "detached on a saved commit, but removing it deletes ignored " + describeIgnored(w.Ignored)}
	case w.HeadInDefault:
		return Verdict{Safe, "detached on a commit already in " + r.DefaultName}
	case w.HeadOnRemote:
		return Verdict{Safe, "detached on a commit that's on a remote branch"}
	default:
		return Verdict{Keep, "detached on a commit that's only on this machine"}
	}
}

// unsaved counts the commits on a branch that exist nowhere but this machine:
// not in a PR head GitHub keeps, and not on any remote branch.
func unsaved(b *Branch) int {
	if b.PR != nil && b.PR.State != "OPEN" && b.BeyondPR >= 0 {
		return min(b.BeyondPR, b.NotOnRemote)
	}
	return b.NotOnRemote
}

// keepNested stops a worktree from being removed when it sits inside a linked worktree that stays,
// such as a submodule worktree inside a Claude session's checkout: removing it would change the parent.
func keepNested(repos []*Repo) {
	var parents []*Worktree
	for _, r := range repos {
		for _, w := range r.Worktrees {
			if !w.Main && !w.Prunable {
				parents = append(parents, w)
			}
		}
	}
	for _, r := range repos {
		for _, w := range r.Worktrees {
			if w.Verdict.Action != Safe || w.Prunable {
				continue
			}
			for _, p := range parents {
				if p != w && p.Verdict.Action != Safe && strings.HasPrefix(w.Path, p.Path+"/") {
					w.Verdict = Verdict{Keep, "inside worktree " + p.Path + ", which is kept"}
				}
			}
		}
	}
}

func classify(r *Repo, keep keepList) {
	for _, b := range r.Branches {
		b.Verdict = classifyBranch(r, b, keep)
	}
	for _, w := range r.Worktrees {
		switch {
		case w.Prunable:
			w.Verdict = Verdict{Safe, "folder is gone, prune the entry"}
		case w.Main:
			w.Verdict = Verdict{Keep, "main checkout"}
		case w.Branch == "":
			w.Verdict = classifyDetached(r, w, keep)
		default:
			for _, b := range r.Branches {
				if b.Name == w.Branch {
					w.Verdict = b.Verdict
				}
			}
			if w.Verdict.Action == "" {
				w.Verdict = Verdict{Keep, "on the default branch"}
			}
		}
	}
}

var irregular = map[string]string{"branch": "branches", "stash": "stashes", "stale entry": "stale entries"}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if p, ok := irregular[word]; ok {
		return fmt.Sprintf("%d %s", n, p)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
