package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func call(tool string, input map[string]any) hookInput {
	raw, _ := json.Marshal(input)
	return hookInput{ToolName: tool, ToolInput: raw}
}

func askConfig(dir string) HookConfig {
	return HookConfig{AllowedTools: askTools, AllowedDir: dir}
}

func TestCheckToolCallAllowsOnlyAskTools(t *testing.T) {
	repo := t.TempDir()
	cfg := askConfig(repo)
	for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "Bash", "PowerShell", "WebFetch", "ToolSearch", "mcp__x__y", ""} {
		if r := checkToolCall(cfg, call(tool, map[string]any{"command": "x"})); r == "" || !strings.Contains(r, "Ask mode") {
			t.Errorf("tool %q: reason %q, want a refusal", tool, r)
		}
	}
	for _, tool := range []string{"Read", "Grep", "Glob"} {
		in := map[string]any{"file_path": "a.go", "pattern": "x"}
		if r := checkToolCall(cfg, call(tool, in)); r != "" {
			t.Errorf("tool %s refused: %q", tool, r)
		}
	}
	// No allowlist configured: no tool limit (and no path check without a dir).
	if r := checkToolCall(HookConfig{}, call("Bash", nil)); r != "" {
		t.Errorf("an empty config refused a tool: %q", r)
	}
}

func TestCheckPathsKeepsReadsInsideTheRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A sibling folder whose name starts with the repo's name.
	sibling := repo + "-other"
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })

	cfg := askConfig(repo)
	ok := []hookInput{
		call("Read", map[string]any{"file_path": filepath.Join(repo, "src", "a.go")}),
		call("Read", map[string]any{"file_path": "src/a.go"}),
		call("Read", map[string]any{"file_path": filepath.Join("src", "..", "b.go")}),
		call("Grep", map[string]any{"pattern": `\.\./secret`, "path": filepath.Join(repo, "src")}), // the pattern is a regex
		call("Grep", map[string]any{"pattern": "x", "glob": "**/*.go"}),
		call("Glob", map[string]any{"pattern": "src/**/*.go"}),
		call("Glob", map[string]any{"pattern": "*.md", "path": repo}),
	}
	for i, in := range ok {
		if r := checkToolCall(cfg, in); r != "" {
			t.Errorf("allowed call %d refused: %q", i, r)
		}
	}

	denied := map[string]hookInput{
		"parent folder":         call("Read", map[string]any{"file_path": filepath.Join("..", "secret.txt")}),
		"deep parent":           call("Read", map[string]any{"file_path": filepath.Join("src", "..", "..", "secret.txt")}),
		"absolute outside":      call("Read", map[string]any{"file_path": filepath.Join(filepath.Dir(repo), "secret.txt")}),
		"sibling with prefix":   call("Read", map[string]any{"file_path": filepath.Join(sibling, "a.go")}),
		"UNC path":              call("Read", map[string]any{"file_path": `\\server\share\a.txt`}),
		"other drive":           call("Read", map[string]any{"file_path": `Z:\a.txt`}),
		"no file named":         call("Read", map[string]any{}),
		"grep path outside":     call("Grep", map[string]any{"pattern": "x", "path": filepath.Dir(repo)}),
		"grep glob parent":      call("Grep", map[string]any{"pattern": "x", "glob": `..\..\*.env`}),
		"glob parent":           call("Glob", map[string]any{"pattern": "../**"}),
		"glob parent backslash": call("Glob", map[string]any{"pattern": `src\..\..\*`}),
		"glob absolute":         call("Glob", map[string]any{"pattern": `C:\Users\*\.ssh\*`}),
		"glob rooted":           call("Glob", map[string]any{"pattern": "/etc/*"}),
		"glob stream":           call("Glob", map[string]any{"pattern": "a.txt:secret"}),
		"glob path outside":     call("Glob", map[string]any{"pattern": "*", "path": filepath.Dir(repo)}),
	}
	for name, in := range denied {
		r := checkToolCall(cfg, in)
		if r == "" {
			t.Errorf("%s: the call was allowed", name)
		}
		if strings.Contains(r, repo) || strings.Contains(r, `\\server`) {
			t.Errorf("%s: the reason shows a path: %q", name, r)
		}
	}

	// Input that is not a JSON object can't be checked, so it is refused.
	if r := checkToolCall(cfg, hookInput{ToolName: "Read", ToolInput: json.RawMessage(`"x"`)}); r == "" {
		t.Error("a non-object tool input was allowed")
	}
}

func TestWithinResolvesShortNamesAndCase(t *testing.T) {
	repo := t.TempDir() // on some machines this is an 8.3 short path
	long, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !within(repo, filepath.Join(long, "a.go")) || !within(long, filepath.Join(repo, "a.go")) {
		t.Error("the short and long form of one folder are not the same folder")
	}
	if filepath.Separator == '\\' {
		if !within(repo, strings.ToUpper(filepath.Join(long, "A.GO"))) {
			t.Error("Windows paths differing only in case are not the same folder")
		}
	}
}

func TestWithinFollowsADirectoryLinkOut(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(repo, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("can't create a link here (Windows needs a privilege for symlinks): %v", err)
	}
	if within(repo, filepath.Join(link, "secret.txt")) {
		t.Error("a link inside the repo led out of it")
	}
}

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
