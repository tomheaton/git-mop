package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loadKeep reads the keep list: "<repo name> <branch>" per line, "path <folder>" to keep a worktree
// or everything under a folder, "throwaway <repo name> <glob>" for branches whose local-only
// commits don't need keeping, "ignorable <glob>" for ignored files that are fine to lose with
// a worktree, or "longlived <branch>" for a branch that PRs merge into and that is never deleted.
// Lines starting with # are comments.
func loadKeep(path, home string) (keepList, error) {
	k := keepList{branches: map[string]bool{}, longLived: map[string]bool{}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return k, nil
	}
	if err != nil {
		return k, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) == 3 && fields[0] == "throwaway" {
			k.throwaway = append(k.throwaway, fields[1]+" "+fields[2])
			continue
		}
		if len(fields) != 2 {
			return k, fmt.Errorf("%s: want \"<repo> <branch>\", \"path <folder>\", \"throwaway <repo> <glob>\", \"ignorable <glob>\" or \"longlived <branch>\", got %q", path, l)
		}
		if fields[0] == "ignorable" {
			k.ignorable = append(k.ignorable, fields[1])
			continue
		}
		if fields[0] == "longlived" {
			k.longLived[fields[1]] = true
			continue
		}
		if fields[0] == "path" {
			p := fields[1]
			if strings.HasPrefix(p, "~/") {
				p = filepath.Join(home, p[2:])
			}
			k.paths = append(k.paths, filepath.Clean(p))
			continue
		}
		k.branches[fields[0]+" "+fields[1]] = true
	}
	return k, sc.Err()
}
