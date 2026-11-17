package main

import "time"

// Repo is one git repository, keyed by its common git dir, with every worktree that shares it.
type Repo struct {
	GitDir       string      `json:"gitDir"`
	Slug         string      `json:"slug,omitempty"` // GitHub owner/name, empty when origin isn't on GitHub
	Default      string      `json:"default"`        // remote-tracking ref of the default branch, e.g. origin/master
	DefaultName  string      `json:"defaultName"`
	Stashes      int         `json:"stashes"`
	DefaultAhead int         `json:"defaultAhead"` // local default branch commits not on origin
	Worktrees    []*Worktree `json:"worktrees"`
	Branches     []*Branch   `json:"branches"`
	FetchError   string      `json:"fetchError,omitempty"`
	GitHubError  string      `json:"githubError,omitempty"`
}

type Worktree struct {
	Path     string   `json:"path"`
	Branch   string   `json:"branch,omitempty"`
	Head     string   `json:"head"`
	Main     bool     `json:"main"`
	Prunable bool     `json:"prunable"`
	Locked   bool     `json:"locked"`
	Dirty    int      `json:"dirty"`
	InUse    []string `json:"inUse,omitempty"` // commands whose working directory is inside the worktree
	// ignored files git would delete with the worktree that can't be rebuilt, such as .env or notes
	Ignored []string `json:"ignored,omitempty"`
	// work inside its submodules that git status doesn't show and removal would delete
	SubmoduleWork string `json:"submoduleWork,omitempty"`
	// for a detached worktree: whether its commit is already in the default branch or on a remote branch
	HeadInDefault bool    `json:"headInDefault"`
	HeadOnRemote  bool    `json:"headOnRemote"`
	Verdict       Verdict `json:"verdict"`
}

type Branch struct {
	Name         string    `json:"name"`
	Tip          string    `json:"tip"`
	Upstream     string    `json:"upstream,omitempty"`
	UpstreamGone bool      `json:"upstreamGone"`
	Ahead        int       `json:"ahead"`       // commits not on its upstream
	NotOnRemote  int       `json:"notOnRemote"` // commits not on any remote-tracking ref
	InDefault    bool      `json:"inDefault"`   // tip is an ancestor of the default branch
	SameContent  bool      `json:"sameContent"` // its combined diff is already in the default branch (squash merge)
	LastCommit   time.Time `json:"lastCommit"`
	Worktree     *Worktree `json:"-"`
	WorktreePath string    `json:"worktree,omitempty"`
	PR           *PR       `json:"pr,omitempty"`
	BeyondPR     int       `json:"beyondPR"` // commits on the branch that aren't in the PR's last head, -1 if unknown
	Stack        string    `json:"stack,omitempty"`
	Verdict      Verdict   `json:"verdict"`
}

type PR struct {
	Number   int       `json:"number"`
	State    string    `json:"state"` // OPEN, MERGED or CLOSED
	Base     string    `json:"base"`
	HeadOid  string    `json:"headOid"`
	Author   string    `json:"author"`
	URL      string    `json:"url"`
	MergedAt time.Time `json:"mergedAt"`
}

type Action string

const (
	Safe   Action = "safe"   // removing it loses nothing
	Review Action = "review" // probably done with, but needs a person to decide
	Keep   Action = "keep"
)

// Verdict is what to do with a branch or worktree. Code is a stable snake_case reason for scripts
// and agents (listed in AGENTS.md); Reason says the same for a person.
type Verdict struct {
	Action Action `json:"action"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}
