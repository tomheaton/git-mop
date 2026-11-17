package main

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
)

type item struct {
	repo   *Repo
	branch *Branch
	wt     *Worktree
	sizeKB int64
}

type plan struct {
	worktrees []item // linked worktrees to remove, with their branch when it's safe too
	branches  []item // branches with no worktree to delete
	prune     []item // worktree entries whose folder is gone
	review    []item
	kept      []item
}

func buildPlan(repos []*Repo) plan {
	var p plan
	for _, r := range repos {
		inWt := map[string]bool{}
		for _, w := range r.Worktrees {
			if w.Branch != "" {
				inWt[w.Branch] = true
			}
			switch {
			case w.Prunable:
				p.prune = append(p.prune, item{repo: r, wt: w})
			case w.Main:
			case w.Verdict.Action == Safe:
				it := item{repo: r, wt: w}
				for _, b := range r.Branches {
					if b.Name == w.Branch && b.Verdict.Action == Safe {
						it.branch = b
					}
				}
				p.worktrees = append(p.worktrees, it)
			case w.Branch == "" && w.Verdict.Action == Review:
				p.review = append(p.review, item{repo: r, wt: w})
			case w.Branch == "" && w.Verdict.Action == Keep:
				p.kept = append(p.kept, item{repo: r, wt: w})
			}
		}
		for _, b := range r.Branches {
			it := item{repo: r, branch: b, wt: b.Worktree}
			switch {
			case b.Verdict.Action == Review:
				p.review = append(p.review, it)
			case b.Verdict.Action == Keep:
				p.kept = append(p.kept, it)
			case b.Verdict.Action == Safe && !inWt[b.Name]:
				p.branches = append(p.branches, it)
			}
		}
	}
	return p
}

// measure fills in disk usage for the worktrees to be removed, in parallel.
func measure(items []item, jobs int) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	for i := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(it *item) {
			defer wg.Done()
			defer func() { <-sem }()
			out, err := run("du", "-sk", it.wt.Path)
			if err == nil {
				f := strings.Fields(out)
				if len(f) > 0 {
					it.sizeKB, _ = strconv.ParseInt(f[0], 10, 64)
				}
			}
		}(&items[i])
	}
	wg.Wait()
}

func printReport(w io.Writer, root string, repos []*Repo, p plan, verbose bool) {
	rel := func(path string) string {
		if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
		// outside the root, such as a worktree in a temp folder: keep the end, which names it
		if len(path) > 60 {
			return ".../" + filepath.Join(filepath.Base(filepath.Dir(filepath.Dir(path))), filepath.Base(filepath.Dir(path)), filepath.Base(path))
		}
		return path
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	var total int64
	for _, it := range p.worktrees {
		total += it.sizeKB
	}
	fmt.Fprintf(w, "git-mop: %d repos under %s\n\n", len(repos), root)

	fmt.Fprintf(w, "SAFE TO REMOVE  (git-mop apply)\n")
	if len(p.worktrees) > 0 {
		fmt.Fprintf(w, "\n  Worktrees: %d, %s by du (pnpm installs share files, so less is freed)\n", len(p.worktrees), human(total))
		sort.SliceStable(p.worktrees, func(i, j int) bool { return p.worktrees[i].sizeKB > p.worktrees[j].sizeKB })
		for _, it := range p.worktrees {
			br := "(detached)"
			if it.wt.Branch != "" {
				br = it.wt.Branch
				if it.branch == nil {
					br += " (branch kept)"
				}
			}
			fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\n", rel(it.wt.Path), short(br, 50), it.wt.Verdict.Reason, human(it.sizeKB))
		}
		tw.Flush()
	}
	if len(p.branches) > 0 {
		fmt.Fprintf(w, "\n  Branches with no worktree: %d\n", len(p.branches))
		sortItems(p.branches)
		for _, it := range p.branches {
			fmt.Fprintf(tw, "    %s\t%s\t%s\n", repoName(it.repo), short(it.branch.Name, 60), it.branch.Verdict.Reason)
		}
		tw.Flush()
	}
	if len(p.prune) > 0 {
		fmt.Fprintf(w, "\n  Stale worktree entries: %d\n", len(p.prune))
		for _, it := range p.prune {
			fmt.Fprintf(tw, "    %s\t%s\n", repoName(it.repo), it.wt.Path)
		}
		tw.Flush()
	}
	if len(p.worktrees)+len(p.branches)+len(p.prune) == 0 {
		fmt.Fprintf(w, "  nothing\n")
	}

	if len(p.review) > 0 {
		fmt.Fprintf(w, "\nREVIEW  (not touched by git-mop apply)\n")
		sortItems(p.review)
		for _, it := range p.review {
			name, why, loc := "(detached)", "", ""
			if it.branch != nil {
				name, why = it.branch.Name, it.branch.Verdict.Reason
			} else {
				why = it.wt.Verdict.Reason
			}
			if it.wt != nil && !it.wt.Main {
				loc = rel(it.wt.Path)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", repoName(it.repo), short(name, 50), why, loc)
		}
		tw.Flush()
	}

	printAttention(w, tw, repos, rel, verbose)

	reasons := map[string]int{}
	for _, it := range p.kept {
		reasons[keptReason(it)]++
	}
	fmt.Fprintf(w, "\nKEPT: %d\n", len(p.kept))
	type rc struct {
		r string
		n int
	}
	var rcs []rc
	for r, n := range reasons {
		rcs = append(rcs, rc{r, n})
	}
	sort.Slice(rcs, func(i, j int) bool { return rcs[i].n > rcs[j].n || (rcs[i].n == rcs[j].n && rcs[i].r < rcs[j].r) })
	for _, x := range rcs {
		fmt.Fprintf(w, "  %4d  %s\n", x.n, x.r)
	}
	if verbose {
		fmt.Fprintln(w)
		sortItems(p.kept)
		for _, it := range p.kept {
			name, why := "(detached)", ""
			if it.branch != nil {
				name, why = it.branch.Name, it.branch.Verdict.Reason
			} else {
				why = it.wt.Verdict.Reason
			}
			loc := ""
			if it.wt != nil {
				loc = rel(it.wt.Path)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", repoName(it.repo), short(name, 50), why, loc)
		}
		tw.Flush()
	} else {
		fmt.Fprintf(w, "  (-v lists them)\n")
	}
}

// printAttention lists things worth knowing that git-mop never changes.
func printAttention(w io.Writer, tw *tabwriter.Writer, repos []*Repo, rel func(string) string, verbose bool) {
	var rows []string
	for _, r := range repos {
		if r.Stashes > 0 {
			rows = append(rows, fmt.Sprintf("  %s\t%s\n", repoName(r), plural(r.Stashes, "stash")))
		}
		if r.DefaultAhead > 0 {
			rows = append(rows, fmt.Sprintf("  %s\t%s on local %s not pushed\n", repoName(r), plural(r.DefaultAhead, "commit"), r.DefaultName))
		}
		for _, w := range r.Worktrees {
			if w.Main && w.Dirty > 0 {
				rows = append(rows, fmt.Sprintf("  %s\tmain checkout %s has %s\n", repoName(r), rel(w.Path), plural(w.Dirty, "uncommitted change")))
			}
		}
		var local, unpushed int
		for _, b := range r.Branches {
			if b.Verdict.Action == Safe {
				continue
			}
			if n := unsaved(b); n > 0 && (b.UpstreamGone || b.Upstream == "") {
				local++
				if verbose {
					rows = append(rows, fmt.Sprintf("  %s\t%s: %s only on this machine\n", repoName(r), short(b.Name, 50), plural(n, "commit")))
				}
			} else if b.Ahead > 0 {
				unpushed++
				if verbose {
					rows = append(rows, fmt.Sprintf("  %s\t%s: %s not pushed\n", repoName(r), short(b.Name, 50), plural(b.Ahead, "commit")))
				}
			}
		}
		if !verbose && local > 0 {
			rows = append(rows, fmt.Sprintf("  %s\t%s with commits only on this machine\n", repoName(r), plural(local, "branch")))
		}
		if !verbose && unpushed > 0 {
			rows = append(rows, fmt.Sprintf("  %s\t%s with commits not pushed to their remote branch\n", repoName(r), plural(unpushed, "branch")))
		}
		if r.FetchError != "" {
			rows = append(rows, fmt.Sprintf("  %s\tfetch failed: %s\n", repoName(r), r.FetchError))
		}
		if r.GitHubError != "" {
			rows = append(rows, fmt.Sprintf("  %s\tGitHub lookup failed, branches judged by git alone: %s\n", repoName(r), short(r.GitHubError, 80)))
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(w, "\nNEEDS ATTENTION  (never changed)\n")
	for _, row := range rows {
		fmt.Fprint(tw, row)
	}
	tw.Flush()
}

func keptReason(it item) string {
	var v Verdict
	if it.branch != nil {
		v = it.branch.Verdict
	} else {
		v = it.wt.Verdict
	}
	// group reasons that differ only by a PR number or a count
	r := strings.ReplaceAll(numbers.ReplaceAllString(v.Reason, ""), " ,", ",")
	r = strings.ReplaceAll(r, "changes", "change")
	r = strings.ReplaceAll(r, "commits", "commit")
	return strings.Join(strings.Fields(r), " ")
}

var numbers = regexp.MustCompile(`#\d+|\b\d+\b`)

func sortItems(items []item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := repoName(items[i].repo), repoName(items[j].repo)
		if a != b {
			return a < b
		}
		if items[i].branch != nil && items[j].branch != nil {
			return items[i].branch.Name < items[j].branch.Name
		}
		return false
	})
}

func human(kb int64) string {
	switch {
	case kb >= 1<<20:
		return fmt.Sprintf("%.1fG", float64(kb)/(1<<20))
	case kb >= 1<<10:
		return fmt.Sprintf("%.0fM", float64(kb)/(1<<10))
	default:
		return fmt.Sprintf("%dK", kb)
	}
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
