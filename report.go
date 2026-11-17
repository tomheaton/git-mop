package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// keptReason groups kept reasons that differ only by a PR number or a count.
func keptReason(reason string) string {
	r := strings.ReplaceAll(numbers.ReplaceAllString(reason, ""), " ,", ",")
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
