package main

import (
	"fmt"
	"path"
	"strings"
)

// rebuildableDirs are ignored folders that an install or build recreates, so losing them with a worktree costs nothing.
var rebuildableDirs = map[string]bool{
	"node_modules": true, ".pnpm-store": true, "vendor": true, "dist": true, "build": true, "out": true,
	".next": true, ".nuxt": true, ".output": true, ".svelte-kit": true, ".turbo": true, ".cache": true,
	".parcel-cache": true, ".vite": true, ".vite-cache": true, ".astro": true, ".wrangler": true,
	"coverage": true, "storybook-static": true, "test-results": true, "playwright-report": true, "blob-report": true,
	"target": true, "bin": true, "obj": true, "__pycache__": true, ".venv": true, "venv": true,
	".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true, ".terraform": true, ".gradle": true,
	".react-router": true, "generated": true, ".generated": true, ".source": true,
}

// rebuildablePaths are ignored folders, at the worktree root or inside a package, that an install or the framework recreates.
var rebuildablePaths = []string{".husky/_", "bootstrap/cache", "storage/framework"}

// rebuildableFiles are ignored file name patterns that tools regenerate.
var rebuildableFiles = []string{".DS_Store", "*.tsbuildinfo", "*.log", ".eslintcache", ".phpunit.result.cache", "*.pyc", "next-env.d.ts", ".phpcs-cache"}

// valuableIgnored lists the ignored files and folders in a worktree that removing it would delete
// and that no install or build brings back, minus anything the keep list marks ignorable.
func valuableIgnored(dir string, keep keepList) []string {
	out, err := gitIn(dir, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "--no-empty-directory")
	if err != nil {
		return []string{"(couldn't list ignored files: " + firstLine(err.Error()) + ")"}
	}
	var found []string
	for _, p := range lines(out) {
		if p != "" && !rebuildable(p, keep) {
			found = append(found, p)
		}
	}
	return found
}

func rebuildable(p string, keep keepList) bool {
	clean := strings.TrimSuffix(p, "/")
	base := path.Base(clean)
	for _, seg := range strings.Split(clean, "/") {
		if rebuildableDirs[seg] {
			return true
		}
	}
	for _, g := range rebuildablePaths {
		if within := "/" + clean + "/"; strings.Contains(within, "/"+g+"/") {
			return true
		}
	}
	for _, g := range rebuildableFiles {
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	for _, g := range keep.ignorable {
		g = strings.TrimSuffix(g, "/")
		if ok, _ := path.Match(g, clean); ok {
			return true
		}
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	return false
}

func describeIgnored(files []string) string {
	if len(files) <= 3 {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(files[:3], ", "), len(files)-3)
}
