package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
)

// synced is what sync did to one branch: fast-forwarded it, or left it and why.
type synced struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Path    string `json:"path,omitempty"` // worktree it's checked out in, empty for a branch with no worktree
	Action  string `json:"action"`         // updated, would update (dry run) or skipped
	Commits int    `json:"commits"`        // commits behind its upstream before the sync
	Reason  string `json:"reason,omitempty"`
}

type syncReport struct {
	Repos         int      `json:"repos"`
	DryRun        bool     `json:"dryRun"`
	Results       []synced `json:"results"`
	FetchFailed   []string `json:"fetchFailed"`
	UpToDate      int      `json:"upToDate"`
	NoUpstream    int      `json:"noUpstream"`
	OtherUpstream int      `json:"otherUpstream"` // branches that track a different branch, such as origin/master
	DetachedWTs   int      `json:"detachedWorktrees"`
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
	if fetch {
		if _, err := git(gitDir, "fetch", "--prune", "--quiet", "--all"); err != nil {
			msg := firstLine(err.Error())
			rep.FetchFailed = append(rep.FetchFailed, name+": "+msg[strings.LastIndex(msg, ": ")+2:])
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
			rep.DetachedWTs++
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
			rep.NoUpstream++
			continue
		}
		// only follow the branch's own remote branch: a feature branch created from origin/master
		// tracks master, and fast-forwarding it there would be a rebase onto master, not a sync
		if _, name, ok := strings.Cut(strings.TrimPrefix(upstream, "refs/remotes/"), "/"); !strings.HasPrefix(upstream, "refs/remotes/") || !ok || name != branch {
			rep.OtherUpstream++
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
			rep.UpToDate++
			continue
		}
		s := synced{Repo: name, Branch: branch, Path: wtOf[branch], Commits: behind, Action: "skipped"}
		switch {
		case ahead > 0:
			s.Reason = fmt.Sprintf("diverged, %d ahead and %d behind", ahead, behind)
		case s.Path != "" && prunable[s.Path]:
			s.Reason = "its worktree folder is gone"
		case s.Path != "":
			st, err := gitIn(s.Path, "status", "--porcelain", "--untracked-files=no")
			users := usersOf(s.Path, cwds)
			switch {
			case err != nil:
				s.Reason = "couldn't read its worktree status"
			case st != "":
				s.Reason = plural(len(lines(st)), "uncommitted change")
			case len(users) > 0 && !includeOpen:
				s.Reason = "open in " + strings.Join(users, ", ") + " (--include-open to update anyway)"
			case dryRun:
				s.Action = "would update"
			default:
				if _, err := gitIn(s.Path, "merge", "--ff-only", "--quiet", upstream); err != nil {
					s.Reason = firstLine(err.Error())
					s.Reason = s.Reason[strings.LastIndex(s.Reason, ": ")+2:]
				} else {
					s.Action = "updated"
				}
			}
		case dryRun:
			s.Action = "would update"
		default:
			// no worktree: move the ref, only if it still points where for-each-ref saw it
			if _, err := git(gitDir, "update-ref", "-m", "git-mop sync: fast-forward", "refs/heads/"+branch, upstream, tip); err != nil {
				s.Reason = firstLine(err.Error())
			} else {
				s.Action = "updated"
			}
		}
		res = append(res, s)
	}
	return res
}

func printSync(w io.Writer, rep syncReport) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	updated := 0
	for _, s := range rep.Results {
		if s.Action == "skipped" {
			continue
		}
		updated++
		loc := "(no worktree)"
		if s.Path != "" {
			loc = tildify(s.Path)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s behind\t%s\n", s.Action, s.Repo, short(s.Branch, 50), plural(s.Commits, "commit"), loc)
	}
	for _, s := range rep.Results {
		if s.Action == "skipped" {
			fmt.Fprintf(tw, "skipped\t%s\t%s\t%s\t%s\n", s.Repo, short(s.Branch, 50), s.Reason, tildify(s.Path))
		}
	}
	tw.Flush()
	for _, f := range rep.FetchFailed {
		fmt.Fprintf(w, "fetch failed  %s\n", f)
	}
	verb := "updated"
	if rep.DryRun {
		verb = "would update"
	}
	fmt.Fprintf(w, "\n%d repos: %s %d, skipped %d, already current %d, fetch failed %d (left alone: %d branches with no upstream, %d tracking another branch, %d detached worktrees)\n",
		rep.Repos, verb, updated, len(rep.Results)-updated, rep.UpToDate, len(rep.FetchFailed), rep.NoUpstream, rep.OtherUpstream, rep.DetachedWTs)
}

func tildify(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}
