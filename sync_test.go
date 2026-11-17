package main

import (
	"os"
	"path/filepath"
	"testing"
)

// sync only ever fast-forwards: anything with its own commits or edits in the way is left and reported.
func TestSyncFastForwardsOnlyBranchesThatAreJustBehind(t *testing.T) {
	root, clone := fixture(t)
	old := sh(t, clone, "rev-parse", "master")

	// each branch tracks its own remote branch, which the other clone then moves on
	for _, b := range []string{"behind", "diverged", "editing"} {
		sh(t, clone, "push", "-q", "origin", "master:"+b)
	}
	sh(t, clone, "fetch", "-q", "origin")
	sh(t, clone, "branch", "--track", "behind", "origin/behind")
	sh(t, clone, "switch", "-qc", "diverged", "--track", "origin/diverged")
	commit(t, clone, "mine")
	sh(t, clone, "switch", "-q", "master")
	editing := filepath.Join(root, "dev", "shop-editing")
	sh(t, clone, "worktree", "add", "-q", "--track", "-b", "editing", editing, "origin/editing")
	os.WriteFile(filepath.Join(editing, "a"), []byte("half done"), 0o644)
	// a feature branch cut from origin/master tracks master: syncing must not drag it onto master
	sh(t, clone, "branch", "--track", "feature", "origin/master")

	// someone else pushes to master
	other := filepath.Join(root, "other")
	sh(t, root, "clone", "-q", filepath.Join(root, "remote.git"), other)
	commit(t, other, "theirs")
	upstream := sh(t, other, "rev-parse", "HEAD")
	for _, b := range []string{"master", "behind", "diverged", "editing"} {
		sh(t, other, "push", "-q", "origin", "HEAD:"+b)
	}

	var rep syncReport
	res := syncRepo(filepath.Join(clone, ".git"), true, false, false, map[string][]string{}, &rep)
	got := map[string]synced{}
	for _, s := range res {
		got[s.Branch] = s
	}
	if got["master"].Action != "updated" || sh(t, clone, "rev-parse", "HEAD") != upstream {
		t.Errorf("clean main checkout on master: want fast-forwarded, got %+v", got["master"])
	}
	if got["behind"].Action != "updated" || sh(t, clone, "rev-parse", "behind") != upstream {
		t.Errorf("branch with no worktree: want its ref moved, got %+v", got["behind"])
	}
	if _, ok := got["feature"]; ok || sh(t, clone, "rev-parse", "feature") != old {
		t.Errorf("a branch tracking another branch must be left alone, got %+v", got["feature"])
	}
	if got["diverged"].Action != "skipped" {
		t.Errorf("a branch with its own commits must not move, got %+v", got["diverged"])
	}
	if got["editing"].Action != "skipped" || sh(t, editing, "rev-parse", "HEAD") != old {
		t.Errorf("a worktree with uncommitted edits must not move, got %+v", got["editing"])
	}

	// a process open in a clean worktree holds it back unless asked
	sh(t, editing, "checkout", "--", "a")
	cwds := map[string][]string{editing: {"node"}}
	res = syncRepo(filepath.Join(clone, ".git"), false, false, false, cwds, &syncReport{})
	for _, s := range res {
		if s.Branch == "editing" && s.Action != "skipped" {
			t.Errorf("open in a process: want skipped, got %+v", s)
		}
	}
	syncRepo(filepath.Join(clone, ".git"), false, true, false, cwds, &syncReport{})
	if sh(t, editing, "rev-parse", "HEAD") != upstream {
		t.Errorf("--include-open should update it")
	}
}

func TestWhereFindsTheWorktreeForABranch(t *testing.T) {
	root, clone := fixture(t)
	dirs, _ := discover(filepath.Join(root, "dev"), 4)

	res := where(dirs, "merged-wt", "")
	if len(res) != 1 || res[0].Path != filepath.Join(root, "dev", "shop-merged") {
		t.Fatalf("want the shop-merged worktree, got %+v", res)
	}
	res = where(dirs, "local", "")
	if len(res) != 1 || res[0].Path != "" {
		t.Errorf("a branch nobody has checked out: want it listed without a path, got %+v", res)
	}
	res = where(dirs, "*-wt", "")
	if len(res) != 2 {
		t.Errorf("glob: want merged-wt and dirty-wt, got %+v", res)
	}
	if res := where(dirs, "master", "other-repo"); len(res) != 0 {
		t.Errorf("repo filter: want nothing outside other-repo, got %+v", res)
	}
	if res := where(dirs, "master", ""); len(res) != 1 || !res[0].Main || res[0].Path != clone {
		t.Errorf("master is in the main checkout, got %+v", res)
	}
}
