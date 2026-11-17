package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// savedPlan is what a scan offered for removal, written to disk so that apply removes exactly
// what the person reviewed and nothing that only became safe later.
type savedPlan struct {
	Created   time.Time       `json:"created"`
	Root      string          `json:"root"`
	Worktrees []plannedWT     `json:"worktrees"`
	Branches  []plannedBranch `json:"branches"`
	Prune     []plannedRepo   `json:"prune"`
}

type plannedRepo struct {
	GitDir   string `json:"gitDir"`
	Slug     string `json:"slug,omitempty"`
	MainPath string `json:"mainPath"`
}

type plannedWT struct {
	plannedRepo
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head"`
	// set when the worktree's branch goes too
	BranchTip string `json:"branchTip,omitempty"`
}

type plannedBranch struct {
	plannedRepo
	Name string `json:"name"`
	Tip  string `json:"tip"`
}

func repoOf(r *Repo) plannedRepo {
	return plannedRepo{GitDir: r.GitDir, Slug: r.Slug, MainPath: r.Worktrees[0].Path}
}

func savePlan(path, root string, p plan) error {
	sp := savedPlan{Created: time.Now(), Root: root}
	for _, it := range p.worktrees {
		w := plannedWT{plannedRepo: repoOf(it.repo), Path: it.wt.Path, Branch: it.wt.Branch, Head: it.wt.Head}
		if it.branch != nil {
			w.BranchTip = it.branch.Tip
		}
		sp.Worktrees = append(sp.Worktrees, w)
	}
	for _, it := range p.branches {
		sp.Branches = append(sp.Branches, plannedBranch{plannedRepo: repoOf(it.repo), Name: it.branch.Name, Tip: it.branch.Tip})
	}
	for _, it := range p.prune {
		sp.Prune = append(sp.Prune, repoOf(it.repo))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// loadPlan reads a saved plan and refuses one older than maxAge, since the person reviewed a world that has moved on.
func loadPlan(path string, maxAge time.Duration) (savedPlan, plan, error) {
	var sp savedPlan
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return sp, plan{}, fmt.Errorf("no saved plan at %s: run git-mop first and review its report", path)
	}
	if err != nil {
		return sp, plan{}, err
	}
	if err := json.Unmarshal(b, &sp); err != nil {
		return sp, plan{}, fmt.Errorf("%s: %v", path, err)
	}
	if age := time.Since(sp.Created); maxAge > 0 && age > maxAge {
		return sp, plan{}, fmt.Errorf("the saved plan is %s old: run git-mop again and review the new report", age.Round(time.Minute))
	}
	repos := map[string]*Repo{}
	repo := func(pr plannedRepo) *Repo {
		if r, ok := repos[pr.GitDir]; ok {
			return r
		}
		r := &Repo{GitDir: pr.GitDir, Slug: pr.Slug, Worktrees: []*Worktree{{Path: pr.MainPath, Main: true}}}
		repos[pr.GitDir] = r
		return r
	}
	var p plan
	for _, w := range sp.Worktrees {
		it := item{repo: repo(w.plannedRepo), wt: &Worktree{Path: w.Path, Branch: w.Branch, Head: w.Head}}
		if w.BranchTip != "" {
			it.branch = &Branch{Name: w.Branch, Tip: w.BranchTip}
		}
		p.worktrees = append(p.worktrees, it)
	}
	for _, b := range sp.Branches {
		p.branches = append(p.branches, item{repo: repo(b.plannedRepo), branch: &Branch{Name: b.Name, Tip: b.Tip}})
	}
	for _, pr := range sp.Prune {
		p.prune = append(p.prune, item{repo: repo(pr)})
	}
	return sp, p, nil
}
