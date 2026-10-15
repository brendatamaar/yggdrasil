package agentstream

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

type hookCall struct {
	// agent is the agent_id of the sub-agent the call is made in.
	event, tool, id, agent string
	input                  map[string]any
	response               map[string]any
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
	if c.agent != "" {
		in["agent_id"], in["agent_type"] = c.agent, "general-purpose"
	}
	if c.response != nil {
		in["tool_response"] = c.response
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

func TestHookDeniesRemoteIsolation(t *testing.T) {
	call := hookCall{event: EventPreToolUse, tool: "Agent", id: "toolu_rm", input: map[string]any{"description": "x", "prompt": "x", "isolation": "remote"}}
	code, _, stderr := runHookCall(t, HookOptions{LogPath: filepath.Join(t.TempDir(), "hook.log"), Mode: DenyExit2}, call.json(t))
	if code != 2 || !strings.Contains(stderr, "isolation: remote") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
}

func TestHookWorktreeCreateAlwaysFails(t *testing.T) {
	in := `{"session_id":"s","transcript_path":"t","cwd":"C:/scratch/acme-shop","hook_event_name":"WorktreeCreate","name":"bold-oak-a3f2"}`
	for _, mode := range []DenyMode{DenyExit2, DenyJSON, DenyJSONLegacy} {
		log := filepath.Join(t.TempDir(), "hook.log")
		// No stdout: the CLI would read a JSON line as the worktree path.
		code, stdout, stderr := runHookCall(t, HookOptions{LogPath: log, Mode: mode, Event: EventWorktreeCreate}, in)
		if code != 2 || stdout != "" || stderr == "" {
			t.Fatalf("%s: code %d, stdout %q, stderr %q", mode, code, stdout, stderr)
		}
		if recs := hookLog(t, log); len(recs) != 1 || recs[0].Decision != DecisionDeny {
			t.Errorf("%s: records = %+v", mode, recs)
		}
	}
}

func TestHookLogOnlyEventsNeverBlock(t *testing.T) {
	stop := `{"session_id":"s","hook_event_name":"SubagentStop","agent_id":"a1","agent_type":"general-purpose"}`
	ok := filepath.Join(t.TempDir(), "hook.log")
	for _, event := range []string{EventSubagentStart, EventSubagentStop, EventPostToolUse, EventPostToolUseFailure} {
		if code, stdout, _ := runHookCall(t, HookOptions{LogPath: ok, Mode: DenyJSON, Event: event}, stop); code != 0 || stdout != "" {
			t.Errorf("%s: code %d, stdout %q", event, code, stdout)
		}
		// Exit 2 on SubagentStop would keep the sub-agent running; a broken
		// log must be a non-blocking error instead.
		broken := filepath.Join(t.TempDir(), "missing", "hook.log")
		if code, stdout, _ := runHookCall(t, HookOptions{LogPath: broken, Mode: DenyJSON, Event: event}, stop); code != 1 || stdout != "" {
			t.Errorf("%s with broken log: code %d, stdout %q; want 1", event, code, stdout)
		}
	}
	if recs := hookLog(t, ok); len(recs) != 4 || recs[1].AgentID != "a1" || recs[1].Decision != DecisionLog {
		t.Errorf("records = %+v", recs)
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

func TestHookAsyncLaunchStaysActive(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hook.log")
	opts := HookOptions{LogPath: log, Mode: DenyExit2, MaxConcurrent: 1}
	spawn := hookCall{event: EventPreToolUse, tool: "Agent", id: "toolu_bg", input: map[string]any{"description": "bg", "run_in_background": true}}
	if code, _, _ := runHookCall(t, opts, spawn.json(t)); code != 0 {
		t.Fatal("first spawn denied")
	}
	post := spawn
	post.event, post.response = EventPostToolUse, map[string]any{"status": "async_launched", "agentId": "a1"}
	if code, _, _ := runHookCall(t, opts, post.json(t)); code != 0 {
		t.Fatal("PostToolUse denied")
	}
	next := spawn
	next.id = "toolu_bg2"
	if code, _, stderr := runHookCall(t, opts, next.json(t)); code != 2 || !strings.Contains(stderr, "at once") {
		t.Fatalf("spawn while a background one runs: code %d, stderr %q", code, stderr)
	}
}

func TestHookDepthCap(t *testing.T) {
	top := hookCall{event: EventPreToolUse, tool: "Agent", id: "toolu_d1", input: map[string]any{}}
	nested := hookCall{event: EventPreToolUse, tool: "Agent", id: "toolu_d2", agent: "a1", input: map[string]any{}}
	for _, tc := range []struct {
		maxDepth   int
		nestedCode int
	}{{1, 2}, {2, 0}, {0, 0}} {
		log := filepath.Join(t.TempDir(), "hook.log")
		opts := HookOptions{LogPath: log, Mode: DenyExit2, MaxDepth: tc.maxDepth}
		if code, _, stderr := runHookCall(t, opts, top.json(t)); code != 0 {
			t.Fatalf("max %d, depth 1: code %d (stderr %q)", tc.maxDepth, code, stderr)
		}
		if code, _, stderr := runHookCall(t, opts, nested.json(t)); code != tc.nestedCode {
			t.Fatalf("max %d, from a sub-agent: code %d, want %d (stderr %q)", tc.maxDepth, code, tc.nestedCode, stderr)
		}
		recs := hookLog(t, log)
		if recs[0].Depth != 1 || recs[1].Depth != 2 || recs[1].AgentID != "a1" {
			t.Errorf("max %d: records = %+v", tc.maxDepth, recs)
		}
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
