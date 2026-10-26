package safety_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanNestedSkipNamedFoldersAreScanned(t *testing.T) {
	s, dir := fsFactory(t, map[string]string{
		"packages/x/build/CLAUDE.md":        "nested",
		"packages/x/build/opencode.json":    "{}",
		"packages/y/node_modules/CLAUDE.md": "nested too",
		"build/CLAUDE.md":                   "root level, skipped",
	})
	sc, err := s.Scan(context.Background(), dir, nil, []string{"CLAUDE.md", "opencode.json"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range sc.Files {
		got = append(got, f.Path)
	}
	want := "packages/x/build/CLAUDE.md packages/x/build/opencode.json packages/y/node_modules/CLAUDE.md"
	if strings.Join(got, " ") != want {
		t.Fatalf("files = %v, want %s", got, want)
	}
}

func TestScanUnreadableConfigAncestorRefuses(t *testing.T) {
	s, dir := fsFactory(t, map[string]string{".claude/agents/a.md": "x", "src/a.go": "y"})
	denyRead(t, filepath.Join(dir, ".claude"))
	_, err := s.Scan(context.Background(), dir, []string{".claude/agents"}, nil)
	if err == nil || !strings.Contains(err.Error(), ".claude") {
		t.Fatalf("err = %v, want a refusal naming .claude", err)
	}
}
