package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A PR from a fork can use the same branch name as a local branch. Only PRs opened from the
// repository itself say anything about a local branch, whoever opened or closed the others.
func TestPRsFromForksAreNotMatchedToLocalBranches(t *testing.T) {
	bin := t.TempDir()
	reply := `{"data":{"repository":{"b0":{"nodes":[
		{"number":7,"state":"MERGED","isCrossRepository":true,"headRefOid":"aaa","baseRefName":"master","mergedAt":"2026-01-02T00:00:00Z"},
		{"number":3,"state":"OPEN","isCrossRepository":false,"headRefOid":"bbb","baseRefName":"master"}
	]}}}}`
	os.WriteFile(filepath.Join(bin, "reply.json"), []byte(reply), 0o644)
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ncat \""+filepath.Join(bin, "reply.json")+"\"\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := lookupPRs("acme/shop", []string{"fix/login"})
	if err != nil {
		t.Fatal(err)
	}
	prs := got["fix/login"]
	if len(prs) != 1 || prs[0].Number != 3 {
		t.Errorf("want only #3 from the repository itself, got %+v", prs)
	}
}
