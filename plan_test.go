package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scanFixture(t *testing.T, clone string, keep keepList) (*Repo, plan) {
	t.Helper()
	r := scanRepo(filepath.Join(clone, ".git"), scanOptions{keep: keep})
	classify(r, keep)
	return r, buildPlan([]*Repo{r})
}

// apply must remove what the person reviewed, never something that only became safe after the report.
func TestApplyRemovesOnlyWhatThePlanListed(t *testing.T) {
	root, clone := fixture(t)
	_, p := scanFixture(t, clone, keepList{})
	planPath := filepath.Join(root, "plan.json")
	if err := savePlan(planPath, root, time.Now(), p); err != nil {
		t.Fatal(err)
	}

	// after the review, another merged worktree appears: safe, but nobody has seen it in a report
	late := filepath.Join(root, "dev", "shop-late")
	sh(t, clone, "branch", "late-wt", "done")
	sh(t, clone, "worktree", "add", "-q", late, "late-wt")

	_, loaded, err := loadPlan(planPath, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	res, err := applyPlan(loaded, filepath.Join(root, "removed.log"), keepList{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dev", "shop-merged")); !os.IsNotExist(err) {
		t.Errorf("want the reviewed worktree removed, results: %+v", res)
	}
	if _, err := os.Stat(late); err != nil {
		t.Errorf("a worktree that wasn't in the reviewed plan must survive: %v", err)
	}
}

func TestStalePlanIsRefused(t *testing.T) {
	root := t.TempDir()
	planPath := filepath.Join(root, "plan.json")
	os.WriteFile(planPath, []byte(`{"created": "2001-01-01T00:00:00Z", "root": "/dev"}`), 0o644)
	if _, _, err := loadPlan(planPath, 24*time.Hour); err == nil || !strings.Contains(err.Error(), "run git-mop again") {
		t.Errorf("want a plan from 2001 refused, got %v", err)
	}
}

// git worktree remove deletes ignored files, so a worktree holding ones nothing rebuilds needs a person.
func TestIgnoredFilesThatCantBeRebuiltBlockRemoval(t *testing.T) {
	root, clone := fixture(t)
	os.WriteFile(filepath.Join(clone, ".git", "info", "exclude"), []byte(".env\nnode_modules/\nnotes/\n"), 0o644)
	merged := filepath.Join(root, "dev", "shop-merged")

	os.MkdirAll(filepath.Join(merged, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(merged, "node_modules", "pkg", "index.js"), []byte("x"), 0o644)
	r, _ := scanFixture(t, clone, keepList{})
	if got := verdicts(r)["merged-wt"]; got != Safe {
		t.Fatalf("node_modules alone comes back with an install: got %s, want safe", got)
	}

	os.WriteFile(filepath.Join(merged, ".env"), []byte("SECRET=1"), 0o644)
	r, _ = scanFixture(t, clone, keepList{})
	if got := verdicts(r)["merged-wt"]; got != Review {
		t.Fatalf("a .env would be lost: got %s, want review", got)
	}

	r, _ = scanFixture(t, clone, keepList{ignorable: []string{".env"}})
	if got := verdicts(r)["merged-wt"]; got != Safe {
		t.Fatalf("an ignorable pattern in the keep file lets it go: got %s, want safe", got)
	}

	// scanned safe, then someone drops notes in it before apply
	_, p := scanFixture(t, clone, keepList{ignorable: []string{".env"}})
	os.MkdirAll(filepath.Join(merged, "notes"), 0o755)
	os.WriteFile(filepath.Join(merged, "notes", "plan.md"), []byte("x"), 0o644)
	res, err := applyPlan(p, filepath.Join(root, "removed.log"), keepList{ignorable: []string{".env"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(merged, "notes", "plan.md")); err != nil {
		t.Errorf("notes added after the scan must survive apply, results: %+v", res)
	}
}

// The plan and the removal log list every repository path and branch name, so only their owner reads them.
func TestPlanAndLogAreOnlyReadableByTheirOwner(t *testing.T) {
	root, clone := fixture(t)
	_, p := scanFixture(t, clone, keepList{})
	state := filepath.Join(root, "state")
	planPath, logPath := filepath.Join(state, "plan.json"), filepath.Join(state, "removed.log")
	if err := savePlan(planPath, root, time.Now(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPlan(p, logPath, keepList{}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{state: 0o700 | os.ModeDir, planPath: 0o600, logPath: 0o600} {
		if fi, err := os.Stat(path); err != nil || fi.Mode() != want {
			t.Errorf("%s: want mode %v, got %v (%v)", filepath.Base(path), want, fi.Mode(), err)
		}
	}
}
