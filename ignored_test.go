package main

import "testing"

// Generated output comes back with an install or build, so it never holds a worktree back.
// Secrets, local config and notes don't come back, so they always do.
func TestRebuildable(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"node_modules/", true},
		{"apps/web/.next/", true},
		{"next-env.d.ts", true},
		{"apps/web/next-env.d.ts", true},
		{"storage/framework/views/abc.php", true},
		{"bootstrap/cache/packages.php", true},
		{"packages/api/bootstrap/cache/services.php", true},
		{".husky/_/", true},
		{".source/", true},
		{".env", false},
		{"apps/web/.env.local", false},
		{".claude/notes/", false},
		{".claude/settings.local.json", false},
		{"storage/oauth-private.key", false},
		{"phpunit.local.xml", false},
		{"shopify.app.toml", false},
		{"gcp-credentials.json", false},
		{"mystorage/framework-notes.md", false},
	}
	for _, c := range cases {
		if got := rebuildable(c.path, keepList{}); got != c.want {
			t.Errorf("rebuildable(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	if !rebuildable(".claude/settings.local.json", keepList{ignorable: []string{".claude/settings.local.json"}}) {
		t.Errorf("an ignorable line in the keep file should let it go")
	}
}
