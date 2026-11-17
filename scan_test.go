package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "add", file)
	sh(t, dir, "commit", "-qm", file)
}

// fixture builds a clone of a local bare remote with one branch in each state the tool must tell apart.
func fixture(t *testing.T) (root, clone string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	root, _ = filepath.EvalSymlinks(t.TempDir())
	remote := filepath.Join(root, "remote.git")
	sh(t, root, "init", "-q", "--bare", "-b", "master", remote)
	clone = filepath.Join(root, "dev", "shop")
	sh(t, root, "clone", "-q", remote, clone)
	commit(t, clone, "a")
	sh(t, clone, "push", "-q", "origin", "master")
	sh(t, clone, "remote", "set-head", "origin", "master")

	// done: merged into master and pushed
	sh(t, clone, "switch", "-qc", "done")
	commit(t, clone, "b")
	sh(t, clone, "switch", "-q", "master")
	sh(t, clone, "merge", "-q", "--ff-only", "done")
	sh(t, clone, "push", "-q", "origin", "master")
	sh(t, clone, "fetch", "-q", "origin")

	// local: a commit that exists only here
	sh(t, clone, "switch", "-qc", "local")
	commit(t, clone, "c")
	sh(t, clone, "switch", "-q", "master")

	// merged-wt: merged, checked out in a clean linked worktree
	sh(t, clone, "branch", "merged-wt", "done")
	sh(t, clone, "worktree", "add", "-q", filepath.Join(root, "dev", "shop-merged"), "merged-wt")

	// dirty-wt: merged, but its worktree has an uncommitted file
	sh(t, clone, "branch", "dirty-wt", "done")
	dirty := filepath.Join(root, "dev", "shop-dirty")
	sh(t, clone, "worktree", "add", "-q", dirty, "dirty-wt")
	os.WriteFile(filepath.Join(dirty, "scratch"), []byte("x"), 0o644)

	// gone: a worktree whose folder was deleted by hand
	gone := filepath.Join(root, "dev", "shop-gone")
	sh(t, clone, "worktree", "add", "-q", "--detach", gone, "master")
	os.RemoveAll(gone)

	// a repo inside node_modules must never be picked up
	nm := filepath.Join(root, "dev", "app", "node_modules", "pkg")
	os.MkdirAll(nm, 0o755)
	sh(t, nm, "init", "-q")
	return root, clone
}

func verdicts(r *Repo) map[string]Action {
	m := map[string]Action{}
	for _, b := range r.Branches {
		m[b.Name] = b.Verdict.Action
	}
	return m
}

func TestScanJudgesBranchesByWhereTheirCommitsLive(t *testing.T) {
	root, clone := fixture(t)
	dirs, err := discover(filepath.Join(root, "dev"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != filepath.Join(clone, ".git") {
		t.Fatalf("discover found %v, want only the clone (worktrees belong to it, node_modules is skipped)", dirs)
	}
	r := scanRepo(dirs[0], scanOptions{})
	classify(r, keepList{})
	got := verdicts(r)
	want := map[string]Action{"done": Safe, "local": Keep, "merged-wt": Safe, "dirty-wt": Keep}
	for b, a := range want {
		if got[b] != a {
			t.Errorf("%s: got %s, want %s", b, got[b], a)
		}
	}
	p := buildPlan([]*Repo{r})
	if len(p.prune) != 1 {
		t.Errorf("want the deleted worktree folder offered for pruning, got %d", len(p.prune))
	}
	if len(p.worktrees) != 1 || p.worktrees[0].wt.Branch != "merged-wt" {
		t.Errorf("want only the clean merged worktree offered for removal, got %+v", p.worktrees)
	}
}

func TestApplySkipsAnythingThatChangedSinceTheScan(t *testing.T) {
	root, clone := fixture(t)
	r := scanRepo(filepath.Join(clone, ".git"), scanOptions{})
	classify(r, keepList{})
	p := buildPlan([]*Repo{r})

	// after the scan, someone commits on "done"
	sh(t, clone, "switch", "-q", "done")
	commit(t, clone, "late")
	sh(t, clone, "switch", "-q", "master")

	var out bytes.Buffer
	if _, err := applyPlan(&out, p, filepath.Join(root, "removed.log"), keepList{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skip shop done: new commits since the scan") {
		t.Errorf("want the moved branch skipped, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "pruned 1 stale entry") {
		t.Errorf("want the deleted worktree folder pruned, got:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "dev", "shop-merged")); !os.IsNotExist(err) {
		t.Errorf("want the clean merged worktree removed")
	}
	if !gitOK(r.GitDir, "rev-parse", "-q", "--verify", "refs/heads/done") {
		t.Errorf("the branch with a late commit must survive")
	}
	if gitOK(r.GitDir, "rev-parse", "-q", "--verify", "refs/heads/merged-wt") {
		t.Errorf("want merged-wt deleted with its worktree")
	}
	log, _ := os.ReadFile(filepath.Join(root, "removed.log"))
	if !strings.Contains(string(log), "merged-wt") {
		t.Errorf("want the removal logged with its SHA, log:\n%s", log)
	}
}
