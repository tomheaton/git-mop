package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const graphqlBatch = 40

type prNode struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	// opened from a fork: its branch name can match a local branch without having anything to do with it
	IsCrossRepository bool      `json:"isCrossRepository"`
	BaseRefName       string    `json:"baseRefName"`
	HeadRefOid        string    `json:"headRefOid"`
	MergedAt          time.Time `json:"mergedAt"`
	URL               string    `json:"url"`
	Author            struct {
		Login string `json:"login"`
	} `json:"author"`
}

// lookupPRs returns, for each head branch name, every PR opened from that branch of the repository
// itself, newest first. PRs from forks with the same branch name are left out.
func lookupPRs(slug string, heads []string) (map[string][]PR, error) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("not a GitHub slug: %s", slug)
	}
	res := map[string][]PR{}
	for start := 0; start < len(heads); start += graphqlBatch {
		end := min(start+graphqlBatch, len(heads))
		var q strings.Builder
		fmt.Fprintf(&q, "query { repository(owner: %s, name: %s) {", strconv.Quote(parts[0]), strconv.Quote(parts[1]))
		for i, h := range heads[start:end] {
			fmt.Fprintf(&q, " b%d: pullRequests(headRefName: %s, first: 10, orderBy: {field: CREATED_AT, direction: DESC}) { nodes { number state isCrossRepository baseRefName headRefOid mergedAt url author { login } } }", i, strconv.Quote(h))
		}
		q.WriteString(" } }")
		out, err := run("gh", "api", "graphql", "-f", "query="+q.String())
		if err != nil {
			// the error repeats the whole query, keep only what gh said
			msg := err.Error()
			return res, fmt.Errorf("gh api graphql: %s", msg[strings.LastIndex(msg, ": ")+2:])
		}
		var body struct {
			Data struct {
				Repository map[string]struct {
					Nodes []prNode `json:"nodes"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			return res, err
		}
		for i, h := range heads[start:end] {
			for _, n := range body.Data.Repository["b"+strconv.Itoa(i)].Nodes {
				if n.IsCrossRepository {
					continue
				}
				res[h] = append(res[h], PR{Number: n.Number, State: n.State, Base: n.BaseRefName, HeadOid: n.HeadRefOid, Author: n.Author.Login, URL: n.URL, MergedAt: n.MergedAt})
			}
		}
	}
	return res, nil
}

// pickPR chooses the PR that decides a branch's fate: an open one if any, otherwise the newest.
func pickPR(prs []PR) *PR {
	if len(prs) == 0 {
		return nil
	}
	for i := range prs {
		if prs[i].State == "OPEN" {
			return &prs[i]
		}
	}
	sort.SliceStable(prs, func(i, j int) bool { return prs[i].Number > prs[j].Number })
	return &prs[0]
}

// mergedPR returns the newest merged PR from a head branch, used to follow a stack down to its base.
func mergedPR(prs []PR) *PR {
	var best *PR
	for i := range prs {
		if prs[i].State == "MERGED" && (best == nil || prs[i].Number > best.Number) {
			best = &prs[i]
		}
	}
	return best
}

// enrichWithPRs attaches PRs to branches, works out commits beyond each PR's head,
// and checks that stacked PRs actually reached the default branch.
func enrichWithPRs(r *Repo, keep keepList) {
	if r.Slug == "" || len(r.Branches) == 0 {
		return
	}
	heads := make([]string, 0, len(r.Branches))
	for _, b := range r.Branches {
		heads = append(heads, b.Name)
	}
	byHead, err := lookupPRs(r.Slug, heads)
	if err != nil {
		r.GitHubError = firstLine(err.Error())
		return
	}
	var need []string
	for _, b := range r.Branches {
		b.PR = pickPR(byHead[b.Name])
		if b.PR != nil && b.PR.State != "OPEN" && !gitOK(r.GitDir, "cat-file", "-e", b.PR.HeadOid+"^{commit}") {
			need = append(need, "refs/pull/"+strconv.Itoa(b.PR.Number)+"/head")
		}
	}
	for start := 0; start < len(need); start += 50 {
		end := min(start+50, len(need))
		git(r.GitDir, append([]string{"fetch", "--quiet", "origin"}, need[start:end]...)...)
	}
	for _, b := range r.Branches {
		if b.PR != nil && b.PR.State != "OPEN" {
			b.BeyondPR = gitCount(r.GitDir, b.PR.HeadOid+".."+b.Tip)
		}
	}
	checkStacks(r, byHead, keep)
}

// checkStacks follows each merged PR's base down to the default branch. A PR merged into a base
// after that base had itself merged never reached the default branch, unless the default branch has its own commit for it.
func checkStacks(r *Repo, byHead map[string][]PR, keep keepList) {
	lookup := func(head string) *PR {
		if _, ok := byHead[head]; !ok {
			prs, err := lookupPRs(r.Slug, []string{head})
			if err != nil {
				return nil
			}
			byHead[head] = prs[head]
		}
		return mergedPR(byHead[head])
	}
	for _, b := range r.Branches {
		if b.PR == nil || b.PR.State != "MERGED" || b.PR.Base == r.DefaultName || keep.isLongLived(b.PR.Base) {
			continue
		}
		cur := b.PR
		for depth := 0; depth < 8; depth++ {
			if cur.Base == r.DefaultName || keep.isLongLived(cur.Base) {
				break
			}
			base := lookup(cur.Base)
			if base == nil {
				b.Stack = "base " + cur.Base + " never merged"
				break
			}
			if !cur.MergedAt.Before(base.MergedAt) && !hasOwnCommit(r, cur.Number) {
				b.Stack = fmt.Sprintf("#%d merged into %s after it had merged, and %s has no commit for it", cur.Number, cur.Base, r.DefaultName)
				break
			}
			cur = base
		}
	}
}

func hasOwnCommit(r *Repo, number int) bool {
	out, _ := git(r.GitDir, "log", "--oneline", "-1", "--fixed-strings", "--grep=(#"+strconv.Itoa(number)+")", r.Default)
	return out != ""
}
