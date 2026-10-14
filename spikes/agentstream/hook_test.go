package agentstream

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type hookCall struct {
	event, tool, id, parent string
	input                   map[string]any
}

func (c hookCall) json(t *testing.T) string {
	t.Helper()
	in := map[string]any{
		"session_id":      "55555555-5555-4555-8555-555555555555",
		"transcript_path": "C:/scratch/transcript.jsonl",
		"cwd":             "C:/scratch/acme-shop",
		"tool_name":       c.tool,
		"tool_input":      c.input,
	}
	if c.event != "" {
		in["hook_event_name"] = c.event
	}
	if c.id != "" {
		in["tool_use_id"] = c.id
	}
	if c.parent != "" {
		in["parent_tool_use_id"] = c.parent
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runHookCall(t *testing.T, opts HookOptions, stdin string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = RunHook(opts, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func hookLog(t *testing.T, path string) []HookRecord {
	t.Helper()
	recs, err := ReadHookLog(path)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

var worktreeSpawn = hookCall{event: EventPreToolUse, tool: "Task", id: "toolu_wt", input: map[string]any{
	"description": "Refactor cart", "prompt": "Refactor src/cart.js", "subagent_type": "general-purpose", "isolation": "worktree",
}}

func TestHookDeniesWorktreeIsolation(t *testing.T) {
	for _, mode := range []DenyMode{DenyExit2, DenyJSON, DenyJSONLegacy} {
		t.Run(string(mode), func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "hook.log")
			code, stdout, stderr := runHookCall(t, HookOptions{LogPath: log, Mode: mode}, worktreeSpawn.json(t))
			switch mode {
			case DenyExit2:
				if code != 2 || stdout != "" || !strings.Contains(stderr, "isolation: worktree") {
					t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
				}
			case DenyJSON:
				var out struct {
					HookSpecificOutput struct {
						HookEventName, PermissionDecision, PermissionDecisionReason string
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal([]byte(stdout), &out); err != nil || code != 0 {
					t.Fatalf("code %d, stdout %q: %v", code, stdout, err)
				}
				h := out.HookSpecificOutput
				if h.HookEventName != EventPreToolUse || h.PermissionDecision != "deny" || h.PermissionDecisionReason == "" {
					t.Fatalf("output = %+v", h)
				}
			case DenyJSONLegacy:
				var out struct{ Decision, Reason string }
				if err := json.Unmarshal([]byte(stdout), &out); err != nil || code != 0 || out.Decision != "block" || out.Reason == "" {
					t.Fatalf("code %d, stdout %q: %v", code, stdout, err)
				}
			}
			recs := hookLog(t, log)
			if len(recs) != 1 {
				t.Fatalf("log has %d records, want 1", len(recs))
			}
			r := recs[0]
			if r.Decision != DecisionDeny || !r.Subagent || r.Isolation != "worktree" || r.Tool != "Task" || r.ToolUseID != "toolu_wt" || len(r.Input) == 0 {
				t.Errorf("record = %+v", r)
			}
		})
	}
}

func TestHookAllowsNormalSpawn(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hook.log")
	opts := HookOptions{LogPath: log, Mode: DenyJSON, MaxDepth: 2, MaxConcurrent: 4}
	for _, tool := range []string{"Task", "Agent"} {
		call := hookCall{event: EventPreToolUse, tool: tool, input: map[string]any{"description": "Review cart", "subagent_type": "general-purpose"}}
		if code, stdout, stderr := runHookCall(t, opts, call.json(t)); code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("%s: code %d, stdout %q, stderr %q", tool, code, stdout, stderr)
		}
	}
	read := hookCall{event: EventPreToolUse, tool: "Read", input: map[string]any{"file_path": "README.md"}}
	if code, _, _ := runHookCall(t, opts, read.json(t)); code != 0 {
		t.Fatalf("Read: code %d", code)
	}
	recs := hookLog(t, log)
	if len(recs) != 3 {
		t.Fatalf("log has %d records, want 3", len(recs))
	}
	for i, r := range recs {
		if r.Decision != DecisionAllow || r.Subagent != (i < 2) {
			t.Errorf("record %d = %+v", i, r)
		}
	}
	if recs[0].Depth != 1 || recs[1].Active != 1 {
		t.Errorf("depth %d, active %d; want 1, 1", recs[0].Depth, recs[1].Active)
	}
}

func TestHookConcurrencyCap(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hook.log")
	opts := HookOptions{LogPath: log, Mode: DenyExit2, MaxConcurrent: 1}
	spawn := func(id string) hookCall {
		return hookCall{event: EventPreToolUse, tool: "Task", id: id, input: map[string]any{"description": id}}
	}
	if code, _, _ := runHookCall(t, opts, spawn("toolu_1").json(t)); code != 0 {
		t.Fatal("first spawn denied")
	}
	if code, _, stderr := runHookCall(t, opts, spawn("toolu_2").json(t)); code != 2 || !strings.Contains(stderr, "at once") {
		t.Fatalf("second spawn: code %d, stderr %q", code, stderr)
	}
	post := spawn("toolu_1")
	post.event = EventPostToolUse
	if code, _, _ := runHookCall(t, opts, post.json(t)); code != 0 {
		t.Fatal("PostToolUse denied")
	}
	if code, _, stderr := runHookCall(t, opts, spawn("toolu_3").json(t)); code != 0 {
		t.Fatalf("spawn after one finished: code %d, stderr %q", code, stderr)
	}
}

func TestHookDepthCap(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hook.log")
	opts := HookOptions{LogPath: log, Mode: DenyExit2, MaxDepth: 2}
	calls := []hookCall{
		{event: EventPreToolUse, tool: "Task", id: "toolu_d1", input: map[string]any{}},
		{event: EventPreToolUse, tool: "Task", id: "toolu_d2", parent: "toolu_d1", input: map[string]any{}},
		{event: EventPreToolUse, tool: "Task", id: "toolu_d3", parent: "toolu_d2", input: map[string]any{}},
	}
	for i, c := range calls {
		code, _, stderr := runHookCall(t, opts, c.json(t))
		if want := map[bool]int{true: 2, false: 0}[i == 2]; code != want {
			t.Fatalf("depth %d: code %d, want %d (stderr %q)", i+1, code, want, stderr)
		}
	}
	recs := hookLog(t, log)
	if recs[2].Depth != 3 || recs[2].Decision != DecisionDeny {
		t.Errorf("third record = %+v", recs[2])
	}
}

func TestHookFailsClosed(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hook.log")
	for name, in := range map[string]string{
		"not json":     "not json",
		"no tool name": `{"hook_event_name":"PreToolUse","tool_input":{}}`,
	} {
		if code, _, stderr := runHookCall(t, HookOptions{LogPath: log, Mode: DenyExit2}, in); code != 2 || stderr == "" {
			t.Errorf("%s: code %d, stderr %q", name, code, stderr)
		}
	}
}

func TestHookEventFlagOverridesInput(t *testing.T) {
	// The input lacks hook_event_name; --event says it is PreToolUse.
	call := worktreeSpawn
	call.event = ""
	log := filepath.Join(t.TempDir(), "hook.log")
	if code, _, _ := runHookCall(t, HookOptions{LogPath: log, Mode: DenyExit2, Event: EventPreToolUse}, call.json(t)); code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
	// As PostToolUse the same input is only logged.
	if code, _, _ := runHookCall(t, HookOptions{LogPath: log, Mode: DenyExit2, Event: EventPostToolUse}, call.json(t)); code != 0 {
		t.Fatalf("PostToolUse: code %d, want 0", code)
	}
	recs := hookLog(t, log)
	if len(recs) != 2 || recs[0].Event != EventPreToolUse || recs[1].Decision != DecisionLog {
		t.Errorf("records = %+v", recs)
	}
}
