package claudecode

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type hookEnv struct {
	t       *testing.T
	cfgPath string
	log     string
	getenv  func(string) string
}

func newHookEnv(t *testing.T, maxDepth, maxConcurrent int) *hookEnv {
	t.Helper()
	dir := t.TempDir()
	h := &hookEnv{t: t, cfgPath: filepath.Join(dir, "hook.json"), log: filepath.Join(dir, "hook.log")}
	data, err := json.Marshal(HookConfig{LogPath: h.log, MaxDepth: maxDepth, MaxConcurrent: maxConcurrent})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h.getenv = func(k string) string {
		if k == HookConfigEnv {
			return h.cfgPath
		}
		return ""
	}
	return h
}

// call runs the hook once and returns its exit code and stderr.
func (h *hookEnv) call(event string, input map[string]any) (int, string) {
	h.t.Helper()
	stdin, err := json.Marshal(input)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.raw([]string{"--event", event}, string(stdin))
}

func (h *hookEnv) raw(args []string, stdin string) (int, string) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	code := RunHook(args, strings.NewReader(stdin), &out, &errOut, h.getenv)
	if out.Len() != 0 {
		h.t.Errorf("hook wrote to stdout: %q", out.String())
	}
	return code, errOut.String()
}

func agentCall(input map[string]any) map[string]any {
	in := map[string]any{"tool_name": "Agent", "tool_input": map[string]any{"description": "d", "prompt": "p"}}
	maps.Copy(in, input)
	return in
}

func TestHookAllowsOrdinaryToolCalls(t *testing.T) {
	h := newHookEnv(t, 2, 4)
	for _, tool := range []string{"Read", "Grep", "Bash", "Edit"} {
		if code, stderr := h.call("PreToolUse", map[string]any{"tool_name": tool, "tool_input": map[string]any{"secret_arg": "S3CRET"}}); code != 0 {
			t.Errorf("%s: exit %d (%s)", tool, code, stderr)
		}
	}
	// The log records what happened but none of the tool's input.
	data, err := os.ReadFile(h.log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "S3CRET") || strings.Count(string(data), "\n") != 4 {
		t.Errorf("hook log = %s", data)
	}
}

func TestHookDeniesAgentNativeIsolation(t *testing.T) {
	h := newHookEnv(t, 2, 4)
	for _, iso := range []string{"worktree", "remote"} {
		code, stderr := h.call("PreToolUse", agentCall(map[string]any{
			"tool_input": map[string]any{"description": "d", "prompt": "p", "isolation": iso},
		}))
		if code != 2 || !strings.Contains(stderr, "isolation") {
			t.Errorf("isolation %s: exit %d, stderr %q", iso, code, stderr)
		}
	}
	if code, _ := h.call("PreToolUse", agentCall(nil)); code != 0 {
		t.Errorf("a plain sub-agent was denied: exit %d", code)
	}
}

func TestHookWorktreeCreateAlwaysDenied(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	code, stderr := h.call("WorktreeCreate", map[string]any{"cwd": "x"})
	if code != 2 || !strings.Contains(stderr, "worktree") {
		t.Errorf("WorktreeCreate: exit %d, stderr %q", code, stderr)
	}
	// Even input it cannot read does not let a worktree through.
	if code, _ := h.raw([]string{"--event", "WorktreeCreate"}, "not json"); code != 2 {
		t.Errorf("WorktreeCreate with unreadable input: exit %d, want 2", code)
	}
}

func TestHookDepthCap(t *testing.T) {
	h := newHookEnv(t, 1, 0)
	if code, _ := h.call("PreToolUse", agentCall(nil)); code != 0 {
		t.Errorf("depth 1 denied: exit %d", code)
	}
	code, stderr := h.call("PreToolUse", agentCall(map[string]any{"agent_id": "a0000000000000001"}))
	if code != 2 || !strings.Contains(stderr, "nesting") {
		t.Errorf("depth 2 with a cap of 1: exit %d, stderr %q", code, stderr)
	}
	h = newHookEnv(t, 0, 0) // no cap
	if code, _ := h.call("PreToolUse", agentCall(map[string]any{"agent_id": "a0000000000000001"})); code != 0 {
		t.Errorf("no cap, but denied: exit %d", code)
	}
}

func TestHookConcurrencyCap(t *testing.T) {
	h := newHookEnv(t, 0, 1)
	if code, _ := h.call("PreToolUse", agentCall(map[string]any{"tool_use_id": "t1"})); code != 0 {
		t.Fatalf("first sub-agent denied: exit %d", code)
	}
	code, stderr := h.call("PreToolUse", agentCall(map[string]any{"tool_use_id": "t2"}))
	if code != 2 || !strings.Contains(stderr, "at once") {
		t.Fatalf("second sub-agent: exit %d, stderr %q", code, stderr)
	}
	// A background launch is still running, so it does not free the slot.
	h.call("PostToolUse", agentCall(map[string]any{"tool_use_id": "t1", "tool_response": map[string]any{"status": "async_launched"}}))
	if code, _ := h.call("PreToolUse", agentCall(map[string]any{"tool_use_id": "t3"})); code != 2 {
		t.Errorf("an async launch freed the slot: exit %d", code)
	}
	// A finished foreground sub-agent does.
	if code, _ := h.call("PostToolUse", agentCall(map[string]any{"tool_use_id": "t1", "tool_response": map[string]any{"status": "completed"}})); code != 0 {
		t.Errorf("PostToolUse exit %d", code)
	}
	if code, _ := h.call("PreToolUse", agentCall(map[string]any{"tool_use_id": "t4"})); code != 0 {
		t.Errorf("after the sub-agent finished: exit %d", code)
	}
}

func TestHookLogOnlyEventsNeverBlock(t *testing.T) {
	h := newHookEnv(t, 1, 1)
	for _, ev := range []string{"PostToolUse", "PostToolUseFailure", "SubagentStart", "SubagentStop"} {
		if code, _ := h.call(ev, agentCall(map[string]any{"tool_input": map[string]any{"isolation": "worktree"}})); code != 0 {
			t.Errorf("%s was blocked: exit %d", ev, code)
		}
		if code, _ := h.raw([]string{"--event", ev}, "garbage"); code != 0 {
			t.Errorf("%s with unreadable input was blocked: exit %d", ev, code)
		}
	}
}

func TestHookFailsClosedOnEnforcingEvents(t *testing.T) {
	h := newHookEnv(t, 1, 1)
	// No tool name, unreadable input: the call is blocked.
	if code, _ := h.call("PreToolUse", map[string]any{"cwd": "x"}); code != 2 {
		t.Errorf("a call without a tool name: exit %d, want 2", code)
	}
	if code, _ := h.raw([]string{"--event", "PreToolUse"}, "garbage"); code != 2 {
		t.Errorf("unreadable input: exit %d, want 2", code)
	}
	// The event name may be missing from the arguments: it is read from the
	// input, and without any it counts as PreToolUse.
	if code, _ := h.raw(nil, `{"hook_event_name":"PreToolUse","tool_name":"Agent","tool_input":{"isolation":"worktree"}}`); code != 2 {
		t.Errorf("event from the input: exit %d, want 2", code)
	}
	if code, _ := h.raw(nil, `{"tool_name":"Agent","tool_input":{"isolation":"worktree"}}`); code != 2 {
		t.Errorf("no event at all: exit %d, want 2", code)
	}
}

func TestHookWithoutConfiguration(t *testing.T) {
	var errOut bytes.Buffer
	none := func(string) string { return "" }
	if code := RunHook([]string{"--event", "PreToolUse"}, strings.NewReader(`{"tool_name":"Read"}`), &errOut, &errOut, none); code != 2 {
		t.Errorf("PreToolUse without configuration: exit %d, want 2", code)
	}
	if code := RunHook([]string{"--event", "WorktreeCreate"}, strings.NewReader(`{}`), &errOut, &errOut, none); code != 2 {
		t.Errorf("WorktreeCreate without configuration: exit %d, want 2", code)
	}
	if code := RunHook([]string{"--event", "PostToolUse"}, strings.NewReader(`{}`), &errOut, &errOut, none); code != 1 {
		t.Errorf("PostToolUse without configuration: exit %d, want 1 (non-blocking)", code)
	}
	bad := filepath.Join(t.TempDir(), "hook.json")
	if err := os.WriteFile(bad, []byte(`{"logPath":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := RunHook([]string{"--event", "PreToolUse"}, strings.NewReader(`{"tool_name":"Read"}`), &errOut, &errOut, func(string) string { return bad }); code != 2 {
		t.Errorf("an invalid configuration: exit %d, want 2", code)
	}
	if strings.Contains(errOut.String(), bad) {
		t.Errorf("the hook message shows a path: %q", errOut.String())
	}
}

func TestHookBlocksWhenItCannotWriteItsLog(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	data, _ := json.Marshal(HookConfig{LogPath: filepath.Join(t.TempDir(), "no", "such", "dir", "hook.log")})
	if err := os.WriteFile(h.cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.call("PreToolUse", map[string]any{"tool_name": "Read"}); code != 2 {
		t.Errorf("a call was allowed although it could not be logged: exit %d", code)
	}
}
