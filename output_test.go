package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// Colour codes take bytes but no screen space, so coloured columns must still line up on screen.
func TestTableAlignsColouredCellsByVisibleWidth(t *testing.T) {
	saved := outStyle
	outStyle = style{true}
	defer func() { outStyle = saved }()

	var tb table
	tb.add(c("✓", outStyle.green), c("api", outStyle.cyan), c("fix/login", outStyle.bold), c("merged PR #12", outStyle.plain))
	tb.add(c("✓", outStyle.green), c("docs-site", outStyle.cyan), c("x", outStyle.plain), c("already in main", outStyle.plain))
	var buf bytes.Buffer
	tb.render(&buf, "")

	plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(buf.String(), "")
	rows := strings.Split(strings.TrimSpace(plain), "\n")
	if len(rows) != 2 || strings.Index(rows[0], "merged") != strings.Index(rows[1], "already") {
		t.Errorf("want the last column aligned on screen, got:\n%s", plain)
	}
}
