package main

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// synced is what sync did to one branch: fast-forwarded it, or left it and why.
type synced struct {
	Repo     string  `json:"repo"`
	RepoPath string  `json:"repoPath"`
	Branch   string  `json:"branch"`
	Path     string  `json:"path,omitempty"` // worktree it's checked out in, empty for a branch with no worktree
	Action   string  `json:"action"`         // updated, would_update (dry run) or skipped
	Behind   int     `json:"behind"`         // commits behind its upstream before the sync
	Upstream string  `json:"upstream"`
	Reason   *Reason `json:"reason,omitempty"` // why it was skipped
}

type fetchFailure struct {
	Repo     string `json:"repo"`
	RepoPath string `json:"repoPath"`
	Error    string `json:"error"`
}

// syncReport is what --json prints for sync.
type syncReport struct {
	header
	DryRun      bool           `json:"dryRun"`
	Summary     syncSummary    `json:"summary"`
	Results     []synced       `json:"results"` // branches that were behind: updated, would_update or skipped
	FetchFailed []fetchFailure `json:"fetchFailed"`
}

type syncSummary struct {
	Repos         int `json:"repos"`
	Updated       int `json:"updated"` // or would update, in a dry run
	Skipped       int `json:"skipped"`
	UpToDate      int `json:"upToDate"`
	FetchFailed   int `json:"fetchFailed"`
	NoUpstream    int `json:"noUpstream"`    // branches with no upstream, or one that's gone
	OtherUpstream int `json:"otherUpstream"` // branches that track a different branch, such as a feature branch on origin/master
	DetachedWTs   int `json:"detachedWorktrees"`
}

var track = regexp.MustCompile(`ahead (\d+)|behind (\d+)`)

// syncRepo fetches a repository and fast-forwards every local branch that is only behind its upstream.
// A checked-out branch moves only when its worktree has no tracked changes and no process open in it,
// unless includeOpen is set. It never merges, rebases or resets.
func syncRepo(gitDir string, fetch, includeOpen, dryRun bool, cwds map[string][]string, rep *syncReport) []synced {
	r := &Repo{GitDir: gitDir}
	if url, err := git(gitDir, "remote", "get-url", "origin"); err == nil {
		if m := githubURL.FindStringSubmatch(url); m != nil {
			r.Slug = m[1] + "/" + m[2]
		}
	}
	name := repoName(r)
	main := strings.TrimSuffix(gitDir, "/.git")
	if fetch {
		if _, err := git(gitDir, "fetch", "--prune", "--quiet", "--all"); err != nil {
			msg := firstLine(err.Error())
			rep.FetchFailed = append(rep.FetchFailed, fetchFailure{name, main, msg[strings.LastIndex(msg, ": ")+2:]})
		}
	}
	wtOf := map[string]string{}
	out, _ := git(gitDir, "worktree", "list", "--porcelain")
	var cur string
	prunable := map[string]bool{}
	for _, l := range lines(out) {
		switch {
		case strings.HasPrefix(l, "worktree "):
			cur = strings.TrimPrefix(l, "worktree ")
		case strings.HasPrefix(l, "branch "):
			wtOf[strings.TrimPrefix(l, "branch refs/heads/")] = cur
		case l == "detached":
			rep.Summary.DetachedWTs++
		case strings.HasPrefix(l, "prunable"):
			prunable[cur] = true
		}
	}
	refs, _ := git(gitDir, "for-each-ref", "--format=%(refname:short)%09%(objectname)%09%(upstream)%09%(upstream:track)", "refs/heads")
	var res []synced
	for _, l := range lines(refs) {
		f := strings.Split(l, "\t")
		if len(f) < 4 {
			continue
		}
		branch, tip, upstream, tr := f[0], f[1], f[2], f[3]
		if upstream == "" || tr == "[gone]" {
			rep.Summary.NoUpstream++
			continue
		}
		// only follow the branch's own remote branch: a feature branch created from origin/master
		// tracks master, and fast-forwarding it there would be a rebase onto master, not a sync
		if _, name, ok := strings.Cut(strings.TrimPrefix(upstream, "refs/remotes/"), "/"); !strings.HasPrefix(upstream, "refs/remotes/") || !ok || name != branch {
			rep.Summary.OtherUpstream++
			continue
		}
		ahead, behind := 0, 0
		for _, m := range track.FindAllStringSubmatch(tr, -1) {
			if m[1] != "" {
				ahead, _ = strconv.Atoi(m[1])
			}
			if m[2] != "" {
				behind, _ = strconv.Atoi(m[2])
			}
		}
		if behind == 0 {
			rep.Summary.UpToDate++
			continue
		}
		s := synced{Repo: name, RepoPath: main, Branch: branch, Path: wtOf[branch], Behind: behind, Upstream: strings.TrimPrefix(upstream, "refs/remotes/"), Action: "skipped"}
		switch {
		case ahead > 0:
			s.Reason = &Reason{"diverged", fmt.Sprintf("diverged, %d ahead and %d behind", ahead, behind)}
		case s.Path != "" && prunable[s.Path]:
			s.Reason = &Reason{"worktree_gone", "its worktree folder is gone"}
		case s.Path != "":
			st, err := gitIn(s.Path, "status", "--porcelain", "--untracked-files=no")
			users := usersOf(s.Path, cwds)
			switch {
			case err != nil:
				s.Reason = &Reason{"status_unreadable", "couldn't read its worktree status"}
			case st != "":
				s.Reason = &Reason{"uncommitted_changes", plural(len(lines(st)), "uncommitted change")}
			case len(users) > 0 && !includeOpen:
				s.Reason = &Reason{"in_use", "open in " + strings.Join(users, ", ") + " (--include-open to update anyway)"}
			case dryRun:
				s.Action = "would_update"
			default:
				if _, err := gitIn(s.Path, "merge", "--ff-only", "--quiet", upstream); err != nil {
					msg := firstLine(err.Error())
					s.Reason = &Reason{"git_failed", msg[strings.LastIndex(msg, ": ")+2:]}
				} else {
					s.Action = "updated"
				}
			}
		case dryRun:
			s.Action = "would_update"
		default:
			// no worktree: move the ref, only if it still points where for-each-ref saw it
			if _, err := git(gitDir, "update-ref", "-m", "git-mop sync: fast-forward", "refs/heads/"+branch, upstream, tip); err != nil {
				s.Reason = &Reason{"git_failed", firstLine(err.Error())}
			} else {
				s.Action = "updated"
			}
		}
		res = append(res, s)
	}
	return res
}

func renderSync(w io.Writer, rep syncReport) {
	st := outStyle
	verb := "updated"
	if rep.DryRun {
		verb = "would update"
	}
	fmt.Fprintf(w, "%s  %s\n\n", st.bold("git-mop sync"), st.dim(plural(rep.Summary.Repos, "repository")))
	fmt.Fprintf(w, "  %s    %s    %s    %s\n",
		paintIf(rep.Summary.Updated > 0, st.green, st.dim)(fmt.Sprintf("↑ %d %s", rep.Summary.Updated, verb)),
		paintIf(rep.Summary.Skipped > 0, st.yellow, st.dim)(fmt.Sprintf("– %d skipped", rep.Summary.Skipped)),
		st.dim(fmt.Sprintf("✓ %d already current", rep.Summary.UpToDate)),
		paintIf(rep.Summary.FetchFailed > 0, st.red, st.dim)(fmt.Sprintf("✗ %d fetch failed", rep.Summary.FetchFailed)))

	var moved, skipped []synced
	for _, s := range rep.Results {
		if s.Action == "skipped" {
			skipped = append(skipped, s)
		} else {
			moved = append(moved, s)
		}
	}
	loc := func(s synced) string {
		if s.Path == "" {
			return "(no worktree)"
		}
		return tildify(s.Path)
	}
	if len(moved) > 0 {
		note := "fast-forwarded to their remote branch"
		if rep.DryRun {
			note = "would fast-forward to their remote branch"
		}
		heading(w, strings.ToUpper(verb), note)
		var t table
		for _, s := range moved {
			t.add(c("↑", st.green), c(s.Repo, st.cyan), c(short(s.Branch, 50), st.bold), cell{text: plural(s.Behind, "commit"), paint: st.plain, right: true}, c(loc(s), st.dim))
		}
		t.render(w, "  ")
	}
	if len(skipped) > 0 {
		heading(w, "SKIPPED", "behind, but left alone")
		var t table
		for _, s := range skipped {
			t.add(c("–", st.yellow), c(s.Repo, st.cyan), c(short(s.Branch, 50), st.plain), c(s.Reason.Text, st.plain), c(loc(s), st.dim))
		}
		t.render(w, "  ")
	}
	if len(rep.FetchFailed) > 0 {
		heading(w, "FETCH FAILED", "these were compared against what was last fetched")
		var t table
		for _, f := range rep.FetchFailed {
			t.add(c("✗", st.red), c(f.Repo, st.cyan), c(f.Error, st.plain))
		}
		t.render(w, "  ")
	}
	fmt.Fprintf(w, "\n%s\n", st.dim(fmt.Sprintf("Left alone: %d with no upstream, %d tracking another branch, %d detached worktrees. sync never merges, rebases or resets.",
		rep.Summary.NoUpstream, rep.Summary.OtherUpstream, rep.Summary.DetachedWTs)))
}
