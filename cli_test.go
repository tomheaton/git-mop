package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets a test run the real command line: the test binary re-runs itself as git-mop
// when GIT_MOP_TEST_MAIN is set, so exit codes and stdout are the ones a caller sees.
func TestMain(m *testing.M) {
	if os.Getenv("GIT_MOP_TEST_MAIN") == "1" {
		main()
		os.Exit(exitOK)
	}
	os.Exit(m.Run())
}

func runCLI(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(append(os.Environ(), "GIT_MOP_TEST_MAIN=1"), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

// With --json, a caller reads exactly one JSON document from stdout, whether the command
// worked or not, and tells outcomes apart by ok, error.code and the exit code.
func TestJSONModeAlwaysPrintsOneDocumentOnStdout(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOK   bool
		errCode  string
	}{
		{"bad flag", []string{"--bogus", "--json"}, exitUsage, false, "usage"},
		{"unknown command", []string{"frob", "--json"}, exitUsage, false, "unknown_command"},
		{"missing plan", []string{"apply", "--plan", filepath.Join(root, "none.json"), "--json"}, exitError, false, "plan_missing"},
		{"missing root", []string{"where", "x", "--root", filepath.Join(root, "none"), "--json"}, exitError, false, "root_not_found"},
		{"branch not found", []string{"where", "nope", "--root", root, "--json"}, exitNotFound, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, _, code := runCLI(t, nil, c.args...)
			var doc struct {
				SchemaVersion int                    `json:"schemaVersion"`
				OK            bool                   `json:"ok"`
				Error         *struct{ Code string } `json:"error"`
			}
			dec := json.NewDecoder(strings.NewReader(stdout))
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("stdout isn't JSON: %v\n%s", err, stdout)
			}
			if dec.More() {
				t.Errorf("want one document on stdout, got more:\n%s", stdout)
			}
			if code != c.wantCode || doc.OK != c.wantOK || doc.SchemaVersion != schemaVersion {
				t.Errorf("want exit %d ok=%v schema %d, got exit %d ok=%v schema %d", c.wantCode, c.wantOK, schemaVersion, code, doc.OK, doc.SchemaVersion)
			}
			if c.errCode != "" && (doc.Error == nil || doc.Error.Code != c.errCode) {
				t.Errorf("want error code %q, got %+v", c.errCode, doc.Error)
			}
		})
	}
}

// Output piped into a file or another program carries no colour codes unless asked for.
func TestColourOnlyWhenWantedOrForced(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		env  []string
		want bool
	}{
		{nil, false},
		{[]string{"FORCE_COLOR=1"}, true},
		{[]string{"FORCE_COLOR=1", "NO_COLOR=1"}, false},
	} {
		_, stderr, _ := runCLI(t, c.env, "where", "nope", "--root", filepath.Join(root, "none"))
		if got := strings.Contains(stderr, "\x1b["); got != c.want {
			t.Errorf("env %v: want colour %v, got %q", c.env, c.want, stderr)
		}
	}
}

// An agent picks items from the scan's JSON by id and removes only those with apply --only.
func TestScanIDsSelectItemsForApplyOnly(t *testing.T) {
	root, clone := fixture(t)
	r, p := scanFixture(t, clone, keepList{})
	planPath := filepath.Join(root, "plan.json")
	created := time.Now()
	if err := savePlan(planPath, root, created, p); err != nil {
		t.Fatal(err)
	}
	doc := buildScanDoc(root, planPath, created, []*Repo{r}, p)
	var id string
	for _, x := range doc.Items {
		if x.Action == "remove" && x.Kind == "branch" && x.Branch == "done" {
			id = x.ID
		}
	}
	if id == "" {
		t.Fatalf("want branch done offered for removal, items: %+v", doc.Items)
	}

	_, loaded, err := loadPlan(planPath, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.only([]string{id, "branch:/nowhere:x"}); err == nil || failure("", err).Code != "unknown_id" {
		t.Errorf("an id the plan doesn't hold must fail the whole call, got %v", err)
	}
	only, err := loaded.only([]string{id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyPlan(only, filepath.Join(root, "removed.log"), keepList{}); err != nil {
		t.Fatal(err)
	}
	if gitOK(r.GitDir, "rev-parse", "-q", "--verify", "refs/heads/done") {
		t.Errorf("want branch done deleted")
	}
	if _, err := os.Stat(filepath.Join(root, "dev", "shop-merged")); err != nil {
		t.Errorf("the worktree that wasn't picked must survive: %v", err)
	}
}
