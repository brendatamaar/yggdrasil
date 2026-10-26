package claudecode

import (
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
)

var _ agents.RepoConfigProvider = (*Adapter)(nil)

// scannedOnly are paths the trust scan hashes although ownAgentConfig does
// not refuse them (they can still change what Claude Code runs).
var scannedOnly = []string{
	".claude/skills", ".claude/commands", ".claude/hooks", ".claude/rules", ".claude/output-styles", "CLAUDE.md", "CLAUDE.local.md",
}

var _ agents.ConfigNameProvider = (*Adapter)(nil)

// Claude Code loads nested CLAUDE.md files, so the scan must find them at any depth.
func TestConfigNames(t *testing.T) {
	got := New(Options{}).ConfigNames()
	want := map[string]bool{"CLAUDE.md": true, "CLAUDE.local.md": true, "AGENTS.md": true}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected %q", n)
		}
	}
}

// Every file ownAgentConfig refuses must be in ConfigPaths, or the trust scan
// would miss a file the adapter treats as config; every other listed path
// must be named in scannedOnly, so the list cannot grow unnoticed.
func TestConfigPathsCoverOwnAgentConfig(t *testing.T) {
	paths := New(Options{}).ConfigPaths()
	if len(paths) == 0 {
		t.Fatal("no config paths")
	}
	extra := map[string]bool{}
	for _, p := range scannedOnly {
		extra[p] = true
	}
	for _, p := range paths {
		want := filepath.Join("/r", p)
		exists := func(got string) bool { return got == want }
		got := ownAgentConfig("/r", exists)
		switch {
		case got == p:
		case got == "" && extra[p]:
		default:
			t.Errorf("path %q: ownAgentConfig finds %q and it is not in scannedOnly", p, got)
		}
	}
	for _, own := range []string{".claude/settings.json", ".claude/settings.local.json", ".mcp.json", ".claude/agents"} {
		found := false
		for _, p := range paths {
			found = found || p == own
		}
		if !found {
			t.Errorf("ConfigPaths lacks %q that ownAgentConfig refuses", own)
		}
	}
	for _, p := range scannedOnly {
		found := false
		for _, q := range paths {
			found = found || p == q
		}
		if !found {
			t.Errorf("ConfigPaths lacks %q", p)
		}
	}
}
