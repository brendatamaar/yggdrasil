package claudecode

import (
	"path/filepath"
	"testing"
)

func TestOwnAgentConfig(t *testing.T) {
	present := map[string]bool{}
	exists := func(p string) bool { return present[filepath.ToSlash(p)] }
	if got := ownAgentConfig("/r", exists); got != "" {
		t.Errorf("a plain repo: %q", got)
	}
	for _, name := range []string{".claude/settings.json", ".claude/settings.local.json", ".mcp.json", ".claude/agents"} {
		present = map[string]bool{"/r/" + name: true}
		if got := ownAgentConfig("/r", exists); got != name {
			t.Errorf("%s: got %q", name, got)
		}
	}
	// CLAUDE.md and the commands folder only hold text the agent reads.
	present = map[string]bool{"/r/CLAUDE.md": true, "/r/.claude/commands": true}
	if got := ownAgentConfig("/r", exists); got != "" {
		t.Errorf("harmless files refused: %q", got)
	}
}
