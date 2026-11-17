package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".pnpm-store": true, ".next": true, ".turbo": true,
	"dist": true, "build": true, "target": true, ".cache": true, ".venv": true, "venv": true,
	"__pycache__": true, ".terraform": true, ".gradle": true,
}

// discover walks root down to maxDepth and returns the common git dir of every repository found.
// Linked worktrees and submodule checkouts aren't descended into: their repository lists them.
func discover(root string, maxDepth int) ([]string, error) {
	seen := map[string]bool{}
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable dirs are skipped, not fatal
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (skipDirs[name] || name == ".git") {
			return filepath.SkipDir
		}
		if strings.Count(path, string(os.PathSeparator))-rootDepth > maxDepth {
			return filepath.SkipDir
		}
		dotgit := filepath.Join(path, ".git")
		info, err := os.Lstat(dotgit)
		if err != nil {
			return nil
		}
		if info.IsDir() {
			seen[dotgit] = true
			return nil
		}
		common, linked := commonDirFromFile(path, dotgit)
		if common != "" {
			seen[common] = true
		}
		if linked && path != root {
			return filepath.SkipDir
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out, err
}

// commonDirFromFile reads a .git file ("gitdir: ...") and resolves the repository's common dir.
// linked is true for linked worktrees and submodule checkouts.
func commonDirFromFile(dir, dotgit string) (common string, linked bool) {
	b, err := os.ReadFile(dotgit)
	if err != nil {
		return "", false
	}
	gd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	gd = filepath.Clean(gd)
	if c, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		cd := strings.TrimSpace(string(c))
		if !filepath.IsAbs(cd) {
			cd = filepath.Join(gd, cd)
		}
		return filepath.Clean(cd), true
	}
	return gd, true
}
