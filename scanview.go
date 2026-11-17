package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// scanDoc is the result of a scan: what --json prints, and what the report draws from.
type scanDoc struct {
	header
	Root      string      `json:"root"`
	Plan      planInfo    `json:"plan"`
	Summary   scanSummary `json:"summary"`
	Items     []scanItem  `json:"items"`     // every branch and linked worktree, remove first, then review, then keep
	Attention []notice    `json:"attention"` // things worth knowing that git-mop never changes
	Next      []nextStep  `json:"next"`
}

type planInfo struct {
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"` // apply refuses the plan after this
}

type scanSummary struct {
	Repos           int   `json:"repos"`
	Remove          int   `json:"remove"`
	Review          int   `json:"review"`
	Keep            int   `json:"keep"`
	Attention       int   `json:"attention"`
	RemoveSizeBytes int64 `json:"removeSizeBytes"` // disk use of the worktrees to remove, 0 with --no-sizes
}

// scanItem is one decision: a worktree, a branch with no worktree to remove, or a repository's
// stale worktree entries.
type scanItem struct {
	ID            string   `json:"id"`     // pass to apply --only; stable between a scan and its apply
	Kind          string   `json:"kind"`   // worktree, branch or stale_worktree
	Action        string   `json:"action"` // remove (apply does it), review (a person decides) or keep
	Reason        Reason   `json:"reason"`
	Repo          string   `json:"repo"`
	RepoPath      string   `json:"repoPath"` // the repository's main checkout
	Slug          string   `json:"slug,omitempty"`
	Branch        string   `json:"branch,omitempty"`
	Path          string   `json:"path,omitempty"`   // the worktree, when there is one
	Commit        string   `json:"commit,omitempty"` // the branch tip, or a detached worktree's HEAD
	DeletesBranch bool     `json:"deletesBranch,omitempty"`
	SizeBytes     int64    `json:"sizeBytes,omitempty"`
	PR            *prRef   `json:"pr,omitempty"`
	Uncommitted   int      `json:"uncommittedChanges,omitempty"`
	OpenIn        []string `json:"openIn,omitempty"`
	IgnoredFiles  []string `json:"ignoredFiles,omitempty"`
	OnlyHere      int      `json:"commitsOnlyHere,omitempty"` // commits on no remote branch and in no PR GitHub keeps
	NotPushed     int      `json:"commitsNotPushed,omitempty"`
	LastCommitAt  string   `json:"lastCommitAt,omitempty"`
}

type prRef struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Base   string `json:"base"`
	URL    string `json:"url,omitempty"`
}

// notice is something worth a person's attention that git-mop leaves alone.
type notice struct {
	Code     string `json:"code"`
	Text     string `json:"text"`
	Repo     string `json:"repo"`
	RepoPath string `json:"repoPath"`
	Branch   string `json:"branch,omitempty"`
	Count    int    `json:"count,omitempty"`
}

var actionWord = map[Action]string{Safe: "remove", Review: "review", Keep: "keep"}

func buildScanDoc(root, planPath string, created time.Time, repos []*Repo, p plan) scanDoc {
	d := scanDoc{
		header:    okHeader(),
		Root:      root,
		Plan:      planInfo{planPath, created, created.Add(24 * time.Hour)},
		Items:     []scanItem{},
		Attention: []notice{},
		Next:      []nextStep{},
	}
	d.Summary.Repos = len(repos)
	sortItems(p.review)
	sortItems(p.kept)

	for _, it := range p.worktrees {
		x := baseItem(it.repo)
		x.ID, x.Kind, x.Action, x.Path, x.Branch, x.Commit = worktreeID(it.wt.Path), "worktree", "remove", it.wt.Path, it.wt.Branch, it.wt.Head
		x.Reason = Reason{it.wt.Verdict.Code, it.wt.Verdict.Reason}
		x.DeletesBranch = it.branch != nil
		x.SizeBytes = it.sizeKB * 1024
		if it.branch != nil {
			x.PR = prOf(it.branch)
		}
		d.Summary.RemoveSizeBytes += x.SizeBytes
		d.Items = append(d.Items, x)
	}
	for _, it := range p.branches {
		d.Items = append(d.Items, branchItem(it, "remove"))
	}
	seen := map[string]bool{}
	for _, it := range p.prune {
		if seen[it.repo.GitDir] {
			continue
		}
		seen[it.repo.GitDir] = true
		x := baseItem(it.repo)
		var paths []string
		for _, w := range it.repo.Worktrees {
			if w.Prunable {
				paths = append(paths, w.Path)
			}
		}
		x.ID, x.Kind, x.Action, x.Path = pruneID(it.repo), "stale_worktree", "remove", strings.Join(paths, ", ")
		x.Reason = Reason{"stale_worktree", fmt.Sprintf("%s whose folder is gone", plural(len(paths), "worktree entry"))}
		d.Items = append(d.Items, x)
	}
	// group what goes by repository, worktrees first and the biggest first
	rank := map[string]int{"worktree": 0, "branch": 1, "stale_worktree": 2}
	sort.SliceStable(d.Items, func(i, j int) bool {
		a, b := d.Items[i], d.Items[j]
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if rank[a.Kind] != rank[b.Kind] {
			return rank[a.Kind] < rank[b.Kind]
		}
		return a.SizeBytes > b.SizeBytes
	})
	d.Summary.Remove = len(d.Items)
	for _, it := range p.review {
		d.Items = append(d.Items, branchItem(it, "review"))
		d.Summary.Review++
	}
	for _, it := range p.kept {
		d.Items = append(d.Items, branchItem(it, "keep"))
		d.Summary.Keep++
	}
	d.Attention = attention(repos)
	d.Summary.Attention = len(d.Attention)
	if d.Summary.Remove > 0 {
		d.Next = append(d.Next,
			nextStep{"git-mop apply", fmt.Sprintf("remove the %s with action \"remove\"", plural(d.Summary.Remove, "item"))},
			nextStep{"git-mop apply --only <id>[,<id>...]", "remove only some of them, by id"})
	}
	return d
}

func baseItem(r *Repo) scanItem {
	return scanItem{Repo: repoName(r), RepoPath: repoPath(r), Slug: r.Slug}
}

// branchItem describes a branch, or a detached worktree when the item has no branch.
func branchItem(it item, action string) scanItem {
	x := baseItem(it.repo)
	x.Action = action
	if it.branch == nil {
		w := it.wt
		x.ID, x.Kind, x.Path, x.Commit = worktreeID(w.Path), "worktree", w.Path, w.Head
		x.Reason = Reason{w.Verdict.Code, w.Verdict.Reason}
		x.Uncommitted, x.OpenIn, x.IgnoredFiles = w.Dirty, w.InUse, w.Ignored
		return x
	}
	b := it.branch
	x.ID, x.Kind, x.Branch, x.Commit = branchID(it.repo, b.Name), "branch", b.Name, b.Tip
	x.Reason = Reason{b.Verdict.Code, b.Verdict.Reason}
	x.PR = prOf(b)
	x.OnlyHere, x.NotPushed = max(unsaved(b), 0), max(b.Ahead, 0)
	if !b.LastCommit.IsZero() {
		x.LastCommitAt = b.LastCommit.UTC().Format(time.RFC3339)
	}
	if w := it.wt; w != nil {
		x.Path = w.Path
		x.Uncommitted, x.OpenIn, x.IgnoredFiles = w.Dirty, w.InUse, w.Ignored
	}
	return x
}

func prOf(b *Branch) *prRef {
	if b.PR == nil {
		return nil
	}
	return &prRef{b.PR.Number, b.PR.State, b.PR.Base, b.PR.URL}
}

// attention lists what a person should know about that git-mop never changes.
func attention(repos []*Repo) []notice {
	ns := []notice{}
	for _, r := range repos {
		add := func(code, text, branch string, n int) {
			ns = append(ns, notice{code, text, repoName(r), repoPath(r), branch, n})
		}
		if r.Stashes > 0 {
			add("stashes", plural(r.Stashes, "stash"), "", r.Stashes)
		}
		if r.DefaultAhead > 0 {
			add("default_branch_unpushed", fmt.Sprintf("%s on local %s not pushed", plural(r.DefaultAhead, "commit"), r.DefaultName), r.DefaultName, r.DefaultAhead)
		}
		for _, w := range r.Worktrees {
			if w.Main && w.Dirty > 0 {
				add("main_checkout_dirty", "main checkout has "+plural(w.Dirty, "uncommitted change"), w.Branch, w.Dirty)
			}
		}
		for _, b := range r.Branches {
			if b.Verdict.Action == Safe {
				continue
			}
			if n := unsaved(b); n > 0 && (b.UpstreamGone || b.Upstream == "") {
				add("local_only_commits", fmt.Sprintf("%s: %s only on this machine", b.Name, plural(n, "commit")), b.Name, n)
			} else if b.Ahead > 0 {
				add("unpushed_commits", fmt.Sprintf("%s: %s not pushed", b.Name, plural(b.Ahead, "commit")), b.Name, b.Ahead)
			}
		}
		if r.FetchError != "" {
			add("fetch_failed", "fetch failed: "+r.FetchError, "", 0)
		}
		if r.GitHubError != "" {
			add("github_failed", "GitHub lookup failed, branches judged by git alone: "+r.GitHubError, "", 0)
		}
	}
	return ns
}

// renderScan draws the report for a person.
func renderScan(w io.Writer, d scanDoc, verbose bool) {
	s := outStyle
	fmt.Fprintf(w, "%s  %s\n\n", s.bold("git-mop"), s.dim(fmt.Sprintf("%s in %s", plural(d.Summary.Repos, "repository"), tildify(d.Root))))
	size := ""
	if d.Summary.RemoveSizeBytes > 0 {
		size = s.dim(" · " + human(d.Summary.RemoveSizeBytes/1024))
	}
	fmt.Fprintf(w, "  %s%s    %s    %s    %s\n",
		s.green(fmt.Sprintf("✓ %d to remove", d.Summary.Remove)), size,
		paintIf(d.Summary.Review > 0, s.yellow, s.dim)(fmt.Sprintf("? %d to review", d.Summary.Review)),
		paintIf(d.Summary.Attention > 0, s.red, s.dim)(fmt.Sprintf("! %d to know about", d.Summary.Attention)),
		s.dim(fmt.Sprintf("· %d kept", d.Summary.Keep)))

	byAction := map[string][]scanItem{}
	for _, x := range d.Items {
		byAction[x.Action] = append(byAction[x.Action], x)
	}
	rel := func(p string) string { return relTo(d.Root, p) }

	if xs := byAction["remove"]; len(xs) > 0 {
		heading(w, "TO REMOVE", "git-mop apply removes these")
		var t table
		prev := ""
		for _, x := range xs {
			repo := x.Repo
			if repo == prev {
				repo = ""
			}
			prev = x.Repo
			kind, branch, loc := x.Kind, x.Branch, ""
			switch x.Kind {
			case "worktree":
				loc = rel(x.Path)
				if branch == "" {
					branch = "(detached)"
				} else if !x.DeletesBranch {
					branch += " (branch stays)"
				}
			case "stale_worktree":
				var rels []string
				for _, p := range strings.Split(x.Path, ", ") {
					rels = append(rels, rel(p))
				}
				kind, loc = "stale", strings.Join(rels, ", ")
			}
			sz := ""
			if x.SizeBytes > 0 {
				sz = human(x.SizeBytes / 1024)
			}
			t.add(c("✓", s.green), c(repo, s.cyan), c(kind, s.dim), c(short(branch, 48), s.bold), c(loc, s.plain), cell{text: sz, paint: s.dim, right: true}, c(x.Reason.Text, s.plain))
		}
		t.render(w, "  ")
	}
	if xs := byAction["review"]; len(xs) > 0 {
		heading(w, "TO REVIEW", "apply leaves these for you to decide")
		drawItems(w, xs, "?", s.yellow, rel, s.plain)
	}
	if len(d.Attention) > 0 {
		heading(w, "TO KNOW ABOUT", "git-mop never changes these")
		var t table
		prev := ""
		for _, n := range d.Attention {
			repo := n.Repo
			if repo == prev {
				repo = ""
			}
			prev = n.Repo
			paint := s.plain
			if n.Code == "fetch_failed" || n.Code == "github_failed" || n.Code == "local_only_commits" {
				paint = s.red
			}
			t.add(c("!", s.red), c(repo, s.cyan), c(n.Text, paint))
		}
		t.render(w, "  ")
	}

	kept := byAction["keep"]
	heading(w, "KEPT", fmt.Sprint(len(kept)))
	if verbose {
		drawItems(w, kept, "·", s.dim, rel, s.dim)
	} else if len(kept) > 0 {
		counts := map[string]int{}
		for _, x := range kept {
			counts[keptReason(x.Reason.Text)]++
		}
		type rc struct {
			r string
			n int
		}
		var rcs []rc
		for r, n := range counts {
			rcs = append(rcs, rc{r, n})
		}
		sort.Slice(rcs, func(i, j int) bool { return rcs[i].n > rcs[j].n || (rcs[i].n == rcs[j].n && rcs[i].r < rcs[j].r) })
		var t table
		for _, x := range rcs {
			t.add(cell{text: fmt.Sprint(x.n), paint: s.dim, right: true}, c(x.r, s.dim))
		}
		t.render(w, "  ")
		fmt.Fprintf(w, "  %s\n", s.dim("-v lists each one"))
	}

	fmt.Fprintln(w)
	if d.Summary.Remove == 0 {
		fmt.Fprintln(w, s.dim("Nothing to remove."))
		return
	}
	fmt.Fprintf(w, "%s Run %s to remove the %s marked %s. %s\n",
		s.dim("Nothing was removed."), s.bold("git-mop apply"), plural(d.Summary.Remove, "item"), s.green("✓"),
		s.dim("The plan expires "+d.Plan.ExpiresAt.Format("Mon 15:04")+"."))
}

// drawItems lists review or kept items, one row per branch or detached worktree.
func drawItems(w io.Writer, xs []scanItem, mark string, markPaint func(string) string, rel func(string) string, textPaint func(string) string) {
	s := outStyle
	var t table
	prev := ""
	for _, x := range xs {
		repo := x.Repo
		if repo == prev {
			repo = ""
		}
		prev = x.Repo
		name, loc := x.Branch, ""
		if name == "" {
			name = "(detached)"
		}
		if x.Path != "" && x.Path != x.RepoPath {
			loc = rel(x.Path)
		}
		t.add(c(mark, markPaint), c(repo, s.cyan), c(short(name, 48), textPaint), c(loc, s.dim), c(x.Reason.Text, textPaint))
	}
	t.render(w, "  ")
}

func paintIf(cond bool, yes, no func(string) string) func(string) string {
	if cond {
		return yes
	}
	return no
}
