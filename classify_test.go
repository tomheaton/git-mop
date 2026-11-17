package main

import "testing"

func testRepo() *Repo {
	return &Repo{Slug: "acme/shop", Default: "origin/master", DefaultName: "master"}
}

func merged(n int) *PR { return &PR{Number: n, State: "MERGED", Base: "master"} }

// The rule that matters: a branch is safe only when every commit on it survives somewhere else.
func TestClassifyBranch(t *testing.T) {
	linked := &Worktree{Path: "/dev/shop-feature"}
	cases := []struct {
		name string
		b    Branch
		keep keepList
		want Action
	}{
		{"merged PR matching its head is safe", Branch{PR: merged(1), BeyondPR: 0}, keepList{}, Safe},
		{"commits added after the merge are kept", Branch{PR: merged(1), BeyondPR: 2}, keepList{}, Keep},
		{"a merged PR whose head can't be compared is kept", Branch{PR: merged(1), BeyondPR: -1}, keepList{}, Keep},
		{"a stack that never reached the default branch is kept", Branch{PR: merged(1), BeyondPR: 0, Stack: "base x never merged"}, keepList{}, Keep},
		{"an open PR is kept", Branch{PR: &PR{Number: 2, State: "OPEN"}}, keepList{}, Keep},
		{"a closed PR matching its head is safe, GitHub keeps it", Branch{PR: &PR{Number: 3, State: "CLOSED"}, BeyondPR: 0}, keepList{}, Safe},
		{"a closed PR with later commits is kept", Branch{PR: &PR{Number: 3, State: "CLOSED"}, BeyondPR: 1}, keepList{}, Keep},
		{"no PR but already in the default branch is safe", Branch{InDefault: true, BeyondPR: -1}, keepList{}, Safe},
		{"no PR with local-only commits is kept", Branch{NotOnRemote: 4, BeyondPR: -1}, keepList{}, Keep},
		{"no PR but squash-equal content needs a person", Branch{SameContent: true, BeyondPR: -1}, keepList{}, Keep},
		{"a merged branch with a dirty worktree is kept", Branch{PR: merged(1), Worktree: &Worktree{Path: "/x", Dirty: 3}}, keepList{}, Keep},
		{"a merged branch open in a process is kept", Branch{PR: merged(1), Worktree: &Worktree{Path: "/x", InUse: []string{"node"}}}, keepList{}, Keep},
		{"a merged branch in the main checkout needs a switch first", Branch{PR: merged(1), Worktree: &Worktree{Path: "/x", Main: true}}, keepList{}, Review},
		{"the keep list wins over a merged PR", Branch{Name: "fix/login", PR: merged(1)}, keepList{branches: map[string]bool{"shop fix/login": true}}, Keep},
		{"a kept path keeps the branch checked out there", Branch{PR: merged(1), Worktree: linked}, keepList{paths: []string{"/dev/shop-feature"}}, Keep},
		{"a long-lived branch is kept even if it once merged", Branch{Name: "develop", PR: merged(9)}, keepList{}, Keep},
		{"a long-lived branch from the keep list is kept", Branch{Name: "next", PR: merged(9)}, keepList{longLived: map[string]bool{"next": true}}, Keep},
		{"a throwaway branch goes despite local-only commits", Branch{Name: "alpha/pages-1.3.0-alpha.4", NotOnRemote: 1, BeyondPR: -1, Worktree: linked}, keepList{throwaway: []string{"shop alpha/*"}}, Safe},
		{"a throwaway branch with uncommitted changes is still kept", Branch{Name: "alpha/x", NotOnRemote: 1, BeyondPR: -1, Worktree: &Worktree{Path: "/x", Dirty: 1}}, keepList{throwaway: []string{"shop alpha/*"}}, Keep},
		{"a throwaway pattern only applies to its own repo", Branch{Name: "alpha/x", NotOnRemote: 1, BeyondPR: -1}, keepList{throwaway: []string{"other alpha/*"}}, Keep},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := c.b
			got := classifyBranch(testRepo(), &b, c.keep)
			if got.Action != c.want {
				t.Fatalf("got %s (%s), want %s", got.Action, got.Reason, c.want)
			}
			// scripts and agents branch on the code, so every verdict carries one
			if got.Code == "" {
				t.Errorf("verdict %q has no reason code", got.Reason)
			}
		})
	}
}

// A worktree inside another linked worktree that stays would change that parent if removed.
func TestNestedWorktreeFollowsItsParent(t *testing.T) {
	parent := &Worktree{Path: "/dev/claude/wt", Verdict: Verdict{Action: Keep, Reason: "dirty"}}
	child := &Worktree{Path: "/dev/claude/wt/sub", Verdict: Verdict{Action: Safe, Reason: "in default"}}
	sibling := &Worktree{Path: "/dev/claude/wt-other", Verdict: Verdict{Action: Safe, Reason: "in default"}}
	keepNested([]*Repo{{Worktrees: []*Worktree{{Main: true, Path: "/dev"}, parent}}, {Worktrees: []*Worktree{{Main: true}, child, sibling}}})
	if child.Verdict.Action != Keep {
		t.Errorf("child of a kept worktree: got %s, want keep", child.Verdict.Action)
	}
	if sibling.Verdict.Action != Safe {
		t.Errorf("a worktree whose path only shares a prefix isn't nested: got %s, want safe", sibling.Verdict.Action)
	}
}

func TestClassifyDetached(t *testing.T) {
	cases := []struct {
		name string
		w    Worktree
		want Action
	}{
		{"commit already in the default branch is safe", Worktree{HeadInDefault: true}, Safe},
		{"commit on a remote branch is safe", Worktree{HeadOnRemote: true}, Safe},
		{"commit only on this machine is kept", Worktree{}, Keep},
		{"uncommitted changes are kept even on a merged commit", Worktree{HeadInDefault: true, Dirty: 1}, Keep},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyDetached(testRepo(), &c.w, keepList{})
			if got.Action != c.want {
				t.Fatalf("got %s (%s), want %s", got.Action, got.Reason, c.want)
			}
			if got.Code == "" {
				t.Errorf("verdict %q has no reason code", got.Reason)
			}
		})
	}
}
