package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// submoduleFixture adds a submodule to the fixture clone and one merged linked worktree per name,
// each with the submodule checked out. git keeps a linked worktree's submodule repositories inside
// the worktree's own git dir, so removing the worktree deletes everything in them.
func submoduleFixture(t *testing.T, names ...string) (root, clone string) {
	t.Helper()
	root, clone = fixture(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	lib := filepath.Join(root, "lib.git")
	sh(t, root, "init", "-q", "--bare", "-b", "master", lib)
	seed := filepath.Join(root, "lib-seed")
	sh(t, root, "clone", "-q", lib, seed)
	os.WriteFile(filepath.Join(seed, ".gitignore"), []byte(".env\n"), 0o644)
	sh(t, seed, "add", ".gitignore")
	sh(t, seed, "commit", "-qm", "lib")
	sh(t, seed, "push", "-q", "origin", "master")

	// two submodules, since git submodule status prints one line per submodule
	sh(t, clone, "submodule", "add", "-q", lib, "lib")
	sh(t, clone, "submodule", "add", "-q", lib, "lib2")
	sh(t, clone, "commit", "-qm", "add lib")
	sh(t, clone, "push", "-q", "origin", "master")
	sh(t, clone, "fetch", "-q", "origin")
	for _, n := range names {
		wt := filepath.Join(root, "dev", "shop-"+n)
		sh(t, clone, "worktree", "add", "-q", "-b", n, wt, "master")
		sh(t, wt, "submodule", "update", "-q", "--init")
	}
	return root, clone
}

// Whatever the person's git config hides from git status, a worktree holding work that exists
// nowhere else must never be offered for removal.
func TestWorkHiddenFromStatusKeepsTheWorktree(t *testing.T) {
	cases := map[string]func(t *testing.T, wt string){
		"untracked": func(t *testing.T, wt string) {
			os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("x"), 0o644)
		},
		"sub-commit": func(t *testing.T, wt string) { commit(t, filepath.Join(wt, "lib"), "work") },
		"sub-branch": func(t *testing.T, wt string) {
			sub := filepath.Join(wt, "lib")
			sh(t, sub, "switch", "-qc", "experiment")
			commit(t, sub, "experiment")
			sh(t, sub, "switch", "-q", "--detach", "HEAD~1")
		},
		"sub-stash": func(t *testing.T, wt string) {
			sub := filepath.Join(wt, "lib")
			os.WriteFile(filepath.Join(sub, ".gitignore"), []byte(".env\nmore\n"), 0o644)
			sh(t, sub, "stash", "-q")
		},
		"sub-ignored": func(t *testing.T, wt string) {
			os.WriteFile(filepath.Join(wt, "lib", ".env"), []byte("SECRET=1"), 0o644)
		},
	}
	names := []string{"clean"}
	for n := range cases {
		names = append(names, n)
	}
	root, clone := submoduleFixture(t, names...)
	// settings people use in large repos, which hide these changes from a plain git status
	sh(t, clone, "config", "status.showUntrackedFiles", "no")
	sh(t, clone, "config", "diff.ignoreSubmodules", "all")
	for n, setup := range cases {
		setup(t, filepath.Join(root, "dev", "shop-"+n))
	}

	_, p := scanFixture(t, clone, keepList{})
	var offered []string
	for _, it := range p.worktrees {
		offered = append(offered, filepath.Base(it.wt.Path))
	}
	sort.Strings(offered)
	// shop-merged comes clean from the base fixture; its shop-dirty has an untracked file the config hides
	if strings.Join(offered, ",") != "shop-clean,shop-merged" {
		t.Errorf("want only the clean worktrees offered for removal, got %v", offered)
	}
}

// git refuses to remove a worktree with submodules unless forced, and forcing deletes the
// submodules' repositories, so apply re-checks them and skips any that gained work after the scan.
func TestApplyRemovesCleanSubmoduleWorktreesButSkipsOnesThatGainedWork(t *testing.T) {
	root, clone := submoduleFixture(t, "clean", "late")
	_, p := scanFixture(t, clone, keepList{})
	if len(p.worktrees) != 3 { // shop-merged from the base fixture, plus both of these
		t.Fatalf("want 3 worktrees offered, got %d", len(p.worktrees))
	}

	late := filepath.Join(root, "dev", "shop-late")
	sub := filepath.Join(late, "lib")
	sh(t, sub, "switch", "-qc", "experiment")
	commit(t, sub, "experiment")
	sh(t, sub, "switch", "-q", "--detach", "HEAD~1")

	res, err := applyPlan(p, filepath.Join(root, "removed.log"), keepList{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dev", "shop-clean")); !os.IsNotExist(err) {
		t.Errorf("want the clean worktree with a submodule removed, results: %+v", res)
	}
	if _, err := os.Stat(late); err != nil {
		t.Fatalf("the worktree whose submodule gained a commit must survive: %v\n%+v", err, res)
	}
	got := result(res, worktreeID(late))
	if got.Outcome != "skipped" || got.Reason == nil || got.Reason.Code != "submodule_work" || got.Reason.Text != "lib has 1 commit on no remote branch" {
		t.Errorf("want shop-late skipped for its submodule commit, got %+v", got)
	}
}
