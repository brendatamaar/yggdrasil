package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// The hook end to end: JSON on stdin, exit code and stderr out.
func TestHookJudgesEveryToolCallWithTheTaskPolicy(t *testing.T) {
	h := newHookEnv(t, 2, 4)
	outside := t.TempDir()
	in := func(parts ...string) string { return filepath.Join(append([]string{h.repo}, parts...)...) }
	cases := []struct {
		name  string
		tool  string
		input map[string]any
		want  int
	}{
		{"read inside", "Read", map[string]any{"file_path": in("a.go")}, 0},
		{"read of a file that does not exist yet", "Read", map[string]any{"file_path": in("new", "a.go")}, 0},
		{"read outside", "Read", map[string]any{"file_path": filepath.Join(outside, "secret.txt")}, 2},
		{"read through ..", "Read", map[string]any{"file_path": in("..", filepath.Base(outside), "secret.txt")}, 2},
		{"read of git config", "Read", map[string]any{"file_path": in(".git", "config")}, 2},
		{"grep in the worktree", "Grep", map[string]any{"pattern": "x", "path": h.repo}, 0},
		{"grep outside", "Grep", map[string]any{"pattern": "x", "path": outside}, 2},
		{"glob escaping with ..", "Glob", map[string]any{"pattern": "../**/*.go"}, 2},
		{"glob inside", "Glob", map[string]any{"pattern": "**/*.go"}, 0},
		{"edit inside", "Edit", map[string]any{"file_path": in("a.go")}, 0},
		{"write outside", "Write", map[string]any{"file_path": filepath.Join(outside, "x.txt")}, 2},
		{"write to git", "Write", map[string]any{"file_path": in(".git", "hooks", "pre-commit")}, 2},
		{"write to agent settings", "Write", map[string]any{"file_path": in(".claude", "settings.json")}, 2},
		{"write to .mcp.json", "Edit", map[string]any{"file_path": in(".mcp.json")}, 2},
		{"write to CLAUDE.md", "MultiEdit", map[string]any{"file_path": in("CLAUDE.md")}, 2},
		{"notebook edit outside", "NotebookEdit", map[string]any{"notebook_path": filepath.Join(outside, "n.ipynb")}, 2},
		{"allowlisted command", "Bash", map[string]any{"command": "go test ./..."}, 0},
		{"allowlisted command in PowerShell", "PowerShell", map[string]any{"command": "go vet ./..."}, 0},
		{"command with an extra flag", "Bash", map[string]any{"command": "go test -exec=x ./..."}, 2},
		{"rm", "Bash", map[string]any{"command": "rm -rf x"}, 2},
		{"curl", "Bash", map[string]any{"command": "curl http://example.test"}, 2},
		{"chained command", "Bash", map[string]any{"command": "go test ./... && curl x"}, 2},
		{"web fetch", "WebFetch", map[string]any{"url": "http://example.test"}, 2},
		{"web search", "WebSearch", map[string]any{"query": "x"}, 2},
		{"our own mcp tool, until M1", "mcp__ygg__propose_commit", map[string]any{}, 2},
		{"another mcp server", "mcp__other__tool", map[string]any{}, 2},
		{"unknown tool", "Brand New Tool", map[string]any{}, 2},
		{"sub-agent", "Agent", map[string]any{"description": "d", "prompt": "p"}, 0},
		{"sub-agent with a worktree", "Agent", map[string]any{"description": "d", "isolation": "worktree"}, 2},
		{"sub-agent with unknown isolation", "Task", map[string]any{"isolation": "container"}, 2},
	}
	for _, c := range cases {
		code, stderr := h.call("PreToolUse", h.tool(c.tool, c.input))
		if code != c.want {
			t.Errorf("%s: exit %d, want %d (stderr %q)", c.name, code, c.want, stderr)
		}
		// What the model sees holds no path and nothing of the call.
		for _, leak := range []string{h.repo, outside, "secret.txt", "example.test", "rm -rf"} {
			if strings.Contains(stderr, leak) {
				t.Errorf("%s: stderr shows %q: %q", c.name, leak, stderr)
			}
		}
		if code == 2 && strings.TrimSpace(stderr) == "" {
			t.Errorf("%s: denied without a reason", c.name)
		}
	}
	// A sub-agent's own tool calls are held to the same policy.
	sub := h.tool("Write", map[string]any{"file_path": filepath.Join(outside, "x.txt")})
	sub["agent_id"] = "a0000000000000001"
	if code, _ := h.call("PreToolUse", sub); code != 2 {
		t.Errorf("write outside from a sub-agent: exit %d, want 2", code)
	}
}

func TestHookAskPolicyIsReadOnly(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	h.writePolicy(policyFor(t, core.ModeAsk, h.repo))
	file := filepath.Join(h.repo, "a.go")
	for _, c := range []struct {
		tool  string
		input map[string]any
		want  int
	}{
		{"Read", map[string]any{"file_path": file}, 0},
		{"Grep", map[string]any{"pattern": "x"}, 0},
		{"Edit", map[string]any{"file_path": file}, 2},
		{"Write", map[string]any{"file_path": file}, 2},
		{"Bash", map[string]any{"command": "go test ./..."}, 2},
		{"PowerShell", map[string]any{"command": "go test ./..."}, 2},
	} {
		if code, stderr := h.call("PreToolUse", h.tool(c.tool, c.input)); code != c.want {
			t.Errorf("%s in Ask mode: exit %d, want %d (%q)", c.tool, code, c.want, stderr)
		}
	}
}

// Anything wrong with the policy blocks every enforcing call (fail closed).
func TestHookFailsClosedWithoutAUsablePolicy(t *testing.T) {
	read := func(h *hookEnv) map[string]any {
		return h.tool("Read", map[string]any{"file_path": filepath.Join(h.repo, "a.go")})
	}
	good := func(h *hookEnv) { // the control: the same call passes with a good policy
		if code, stderr := h.call("PreToolUse", read(h)); code != 0 {
			t.Fatalf("control call denied: exit %d (%s)", code, stderr)
		}
	}
	type mutate func(h *hookEnv)
	cases := map[string]mutate{
		"no policy path in the config": func(h *hookEnv) { h.writeConfig(HookConfig{LogPath: h.log}) },
		"policy file missing":          func(h *hookEnv) { _ = os.Remove(h.policy) },
		"policy path is a folder":      func(h *hookEnv) { h.writeConfig(HookConfig{LogPath: h.log, PolicyPath: filepath.Dir(h.policy)}) },
		"policy is not JSON":           func(h *hookEnv) { _ = os.WriteFile(h.policy, []byte("not json"), 0o600) },
		"policy is empty":              func(h *hookEnv) { _ = os.WriteFile(h.policy, nil, 0o600) },
		"policy has an unknown mode": func(h *hookEnv) {
			_ = os.WriteFile(h.policy, []byte(`{"mode":"yolo","readRoots":["/x/y"],"enforcement":"hook"}`), 0o600)
		},
		"policy has no read root": func(h *hookEnv) {
			p := policyFor(t, core.ModeCode, h.repo)
			p.ReadRoots, p.WriteRoots = nil, nil
			_ = os.WriteFile(h.policy, guard.FormatPolicy(p), 0o600)
		},
		"policy has a relative root": func(h *hookEnv) {
			_ = os.WriteFile(h.policy, []byte(`{"mode":"code","readRoots":["repo"],"writeRoots":["repo"],"enforcement":"hook"}`), 0o600)
		},
		"policy has an unknown field": func(h *hookEnv) {
			p := string(guard.FormatPolicy(policyFor(t, core.ModeCode, h.repo)))
			_ = os.WriteFile(h.policy, []byte(strings.TrimSuffix(p, "}")+`,"allowEverything":true}`), 0o600)
		},
		"no config at all": func(h *hookEnv) { h.getenv = func(string) string { return "" } },
	}
	for name, mutate := range cases {
		h := newHookEnv(t, 0, 0)
		good(h)
		mutate(h)
		for _, in := range []map[string]any{
			read(h),
			h.tool("Agent", map[string]any{"prompt": "p"}), // even a sub-agent
		} {
			code, stderr := h.call("PreToolUse", in)
			if code != 2 {
				t.Errorf("%s: exit %d, want 2", name, code)
			}
			if strings.Contains(stderr, h.policy) || strings.Contains(stderr, h.repo) {
				t.Errorf("%s: stderr shows a path: %q", name, stderr)
			}
		}
	}
	// Bad stdin: not JSON, no tool, and a tool input of the wrong shape.
	h := newHookEnv(t, 0, 0)
	for _, stdin := range []string{"", "not json", `{}`, `{"tool_name":"Read","tool_input":"x","cwd":"x"}`, `{"tool_name":"Read","tool_input":{"file_path":5}}`} {
		if code, _ := h.raw([]string{"--event", "PreToolUse"}, stdin); code != 2 {
			t.Errorf("stdin %q: exit %d, want 2", stdin, code)
		}
	}
	// Events that only log never block, even without a policy.
	h = newHookEnv(t, 0, 0)
	_ = os.Remove(h.policy)
	for _, ev := range []string{"PostToolUse", "SubagentStart", "SubagentStop"} {
		if code, _ := h.call(ev, h.tool("Read", map[string]any{})); code != 0 {
			t.Errorf("%s without a policy: exit %d, want 0", ev, code)
		}
	}
	// WorktreeCreate is denied whatever the policy says.
	if code, _ := h.call("WorktreeCreate", map[string]any{}); code != 2 {
		t.Errorf("WorktreeCreate: exit %d, want 2", code)
	}
}
