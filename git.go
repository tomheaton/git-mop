package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// noRepoPrograms stops git running programs a repository's own config names: an fsmonitor on
// every status, and hooks on branch deletes and merges. git-mop runs git in every repository under
// the root, including ones that arrived with their .git folder, so it doesn't run their code.
var noRepoPrograms = []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}

// git runs a git command against a repository's common dir and returns trimmed stdout.
func git(gitDir string, args ...string) (string, error) {
	return run("git", append(append([]string{"--git-dir=" + gitDir}, noRepoPrograms...), args...)...)
}

// gitIn runs a git command inside a worktree.
func gitIn(dir string, args ...string) (string, error) {
	return run("git", append(append([]string{"-C", dir}, noRepoPrograms...), args...)...)
}

// statusIn lists everything a removal would lose that git tracks or could track, whatever the
// person's config hides: untracked files, and changes and new commits inside submodules.
func statusIn(dir string) (string, error) {
	return gitIn(dir, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=none")
}

// noPrompt stops git and ssh from asking for credentials on the terminal: a remote that needs them fails instead of hanging the scan.
var noPrompt = []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "GCM_INTERACTIVE=never"}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), noPrompt...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return strings.TrimSpace(out.String()), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func gitOK(gitDir string, args ...string) bool {
	_, err := git(gitDir, args...)
	return err == nil
}

func gitCount(gitDir string, args ...string) int {
	out, err := git(gitDir, append([]string{"rev-list", "--count"}, args...)...)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return -1
	}
	return n
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
