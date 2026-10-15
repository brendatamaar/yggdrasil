package agentstream

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func parseFile(t *testing.T, name string) []Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	var events []Event
	if err := Parse(f, func(ev Event) bool { events = append(events, ev); return true }); err != nil {
		t.Fatal(err)
	}
	return events
}

func kinds(events []Event) []Kind {
	out := make([]Kind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

func TestParsePlain(t *testing.T) {
	events := parseFile(t, "plain.jsonl")
	want := []Kind{KindInit, KindText, KindToolCall, KindUsage, KindToolResult, KindText, KindUsage, KindResult}
	if got := kinds(events); !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for _, ev := range events {
		if ev.Raw == "" || ev.ParentID != "" {
			t.Errorf("line %d %s: raw %q, parent %q", ev.Line, ev.Kind, ev.Raw, ev.ParentID)
		}
	}
	if ev := events[0]; ev.ID != "11111111-1111-4111-8111-111111111111" || ev.Model != "claude-fictional-haiku" {
		t.Errorf("init = %+v", ev)
	}
	call := events[2]
	if call.ID != "toolu_plain01" || call.Tool == nil || call.Tool.Name != "Read" || !strings.Contains(string(call.Tool.Input), "README.md") {
		t.Errorf("tool call = %+v", call)
	}
	if u := events[3].Usage; u == nil || u.InputTokens != 120 || u.OutputTokens != 30 || u.CacheReadInputTokens != 800 {
		t.Errorf("usage = %+v", u)
	}
	if res := events[4]; res.ResultFor != "toolu_plain01" || !strings.HasPrefix(res.Text, "# acme-shop") {
		t.Errorf("tool result = %+v", res)
	}
	res := events[7]
	if res.Usage == nil || res.Usage.CostUSD == nil || *res.Usage.CostUSD != 0.0021 || res.Usage.NumTurns != 2 || res.Usage.OutputTokens != 42 {
		t.Errorf("result usage = %+v", res.Usage)
	}
}

func TestParseSubagentsNested(t *testing.T) {
	events := parseFile(t, "subagents.jsonl")
	if len(events) != 15 {
		t.Fatalf("got %d events, want 15: %v", len(events), kinds(events))
	}
	byID := map[string]Event{}
	for _, ev := range events {
		if ev.Kind == KindSubagentStart || ev.Kind == KindToolCall {
			byID[ev.ID] = ev
		}
	}
	for id, wantParent := range map[string]string{"toolu_suba": "", "toolu_subb": "toolu_suba", "toolu_subc": "toolu_subb"} {
		ev, ok := byID[id]
		if !ok {
			t.Fatalf("no call %s", id)
		}
		if ev.ParentID != wantParent {
			t.Errorf("%s parent = %q, want %q", id, ev.ParentID, wantParent)
		}
	}
	if byID["toolu_subc"].Kind != KindToolCall || byID["toolu_subb"].Kind != KindSubagentStart {
		t.Errorf("Read must be a tool call and Task a sub-agent start")
	}
	if in := ParseSubagentInput(byID["toolu_suba"].Tool.Input); in.SubagentType != "general-purpose" || in.Description != "Review cart" || in.WantsWorktree() {
		t.Errorf("sub-agent input = %+v", in)
	}

	sum := NewSummary()
	for _, ev := range events {
		sum.Add(ev)
	}
	if len(sum.Subagents) != 2 {
		t.Fatalf("summary sub-agents = %+v", sum.Subagents)
	}
	a, b := sum.Subagents[0], sum.Subagents[1]
	if a.ID != "toolu_suba" || a.Depth != 1 || b.ID != "toolu_subb" || b.ParentID != "toolu_suba" || b.Depth != 2 {
		t.Errorf("tree = %+v, %+v", a, b)
	}
	// suba: text, start, usage (line 3) + tool result, text (lines 7, 8).
	// subb: tool call, tool result, text (lines 4-6).
	if a.Events != 5 || b.Events != 3 {
		t.Errorf("events per sub-agent = %d, %d; want 5, 3", a.Events, b.Events)
	}
	if sum.CostUSD == nil || *sum.CostUSD != 0.0064 {
		t.Errorf("cost = %v", sum.CostUSD)
	}
}

func TestParseTaskStartedAndAgentID(t *testing.T) {
	in := `{"type":"system","subtype":"task_started","task_id":"a5c2e8f41b09d7a36","tool_use_id":"toolu_suba","description":"Review cart","task_type":"local_agent","spawn_depth":1,"session_id":"s"}
{"type":"assistant","message":{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"hi"}]},"parent_tool_use_id":"toolu_suba","agent_id":"a5c2e8f41b09d7a36","session_id":"s"}
`
	var events []Event
	_ = Parse(strings.NewReader(in), func(ev Event) bool { events = append(events, ev); return true })
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if ts := events[0]; ts.Kind != KindTaskStarted || ts.ID != "toolu_suba" || ts.AgentID != "a5c2e8f41b09d7a36" || ts.Depth != 1 || ts.Text != "Review cart" {
		t.Errorf("task_started = %+v", ts)
	}
	if ev := events[1]; ev.ParentID != "toolu_suba" || ev.AgentID != "a5c2e8f41b09d7a36" {
		t.Errorf("sub-agent text = %+v", ev)
	}
	sum := NewSummary()
	sum.Add(Event{Kind: KindSubagentStart, ID: "toolu_suba"})
	sum.Add(events[0])
	if a := sum.Subagents[0]; a.AgentID != "a5c2e8f41b09d7a36" || a.SpawnDepth != 1 {
		t.Errorf("summary = %+v", a)
	}
}

// The live/ fixtures are scrubbed captures from the part 2 live run (CLI
// 2.1.299); *.hook.jsonl are the hook logs of the same runs.
func TestParseLiveFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "live", "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no live fixtures: %v", err)
	}
	for _, path := range files {
		name := strings.TrimPrefix(filepath.ToSlash(path), "testdata/")
		if strings.HasSuffix(path, ".hook.jsonl") {
			recs, err := ReadHookLog(path)
			if err != nil || len(recs) == 0 {
				t.Errorf("%s: %d records, %v", name, len(recs), err)
			}
			continue
		}
		events := parseFile(t, name)
		for _, ev := range events {
			if ev.Kind == KindError {
				t.Errorf("%s line %d: %s", name, ev.Line, ev.Err)
			}
		}
		if events[0].Kind != KindInit {
			t.Errorf("%s: first event %s", name, events[0].Kind)
		}
	}

	events := parseFile(t, "live/subagent-read.jsonl")
	sum := NewSummary()
	for _, ev := range events {
		sum.Add(ev)
	}
	if len(sum.Subagents) != 1 || sum.Subagents[0].SpawnDepth != 1 || sum.Subagents[0].AgentID == "" || sum.Subagents[0].Events == 0 {
		t.Fatalf("sub-agents = %+v", sum.Subagents)
	}
	sub := sum.Subagents[0]
	for _, ev := range events {
		if ev.ParentID != "" && (ev.ParentID != sub.ID || ev.AgentID != sub.AgentID) {
			t.Errorf("line %d: parent %q agent %q, want %q %q", ev.Line, ev.ParentID, ev.AgentID, sub.ID, sub.AgentID)
		}
	}
	recs, err := ReadHookLog(filepath.Join("testdata", "live", "subagent-read.hook.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var subRead bool
	for _, r := range recs {
		if r.Event == EventPreToolUse && r.Tool == "Read" && r.AgentID == sub.AgentID {
			subRead = true
		}
	}
	if !subRead {
		t.Error("no PreToolUse for the sub-agent's Read with its agent_id")
	}

	denied := parseFile(t, "live/dontask-shortpath-denied.jsonl")
	if !slices.ContainsFunc(denied, func(ev Event) bool {
		return ev.Kind == KindPermissionDenied && ev.Tool != nil && ev.Tool.Name == "Read"
	}) {
		t.Error("no permission_denied event for Read")
	}
}

func TestParseKeepsUnknown(t *testing.T) {
	events := parseFile(t, "unknown.jsonl")
	want := []Kind{KindInit, KindUnknown, KindUnknown, KindUnknown, KindText, KindUnknown, KindUnknown, KindResult}
	if got := kinds(events); !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for _, ev := range events {
		if ev.Raw == "" {
			t.Errorf("line %d: raw dropped", ev.Line)
		}
	}
	if !strings.Contains(events[3].Err, "thinking") {
		t.Errorf("thinking block: err = %q", events[3].Err)
	}
	if events[4].Text != "Hello from acme-shop." {
		t.Errorf("text after unknown block = %q", events[4].Text)
	}
}

func TestParseMalformed(t *testing.T) {
	events := parseFile(t, "malformed.jsonl")
	want := []Kind{KindInit, KindError, KindError, KindError, KindText, KindResult}
	if got := kinds(events); !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	lines := []int{1, 2, 3, 5, 6, 7}
	for i, ev := range events {
		if ev.Line != lines[i] {
			t.Errorf("event %d: line %d, want %d", i, ev.Line, lines[i])
		}
		if ev.Kind == KindError && (ev.Err == "" || ev.Raw == "") {
			t.Errorf("line %d: error event without reason or raw: %+v", ev.Line, ev)
		}
	}
}

func TestParseLongLine(t *testing.T) {
	in := strings.Repeat("x", MaxLineBytes+10) + "\n" + `{"type":"result","result":"ok"}` + "\n"
	var events []Event
	if err := Parse(strings.NewReader(in), func(ev Event) bool { events = append(events, ev); return true }); err != nil {
		t.Fatal(err)
	}
	if got := kinds(events); !slices.Equal(got, []Kind{KindError, KindResult}) || events[1].Line != 2 {
		t.Fatalf("events = %v (lines %d, %d)", got, events[0].Line, events[len(events)-1].Line)
	}
}

func TestParseNoTrailingNewline(t *testing.T) {
	var events []Event
	_ = Parse(strings.NewReader(`{"type":"result","result":"ok"}`), func(ev Event) bool { events = append(events, ev); return true })
	if len(events) != 1 || events[0].Kind != KindResult || events[0].Text != "ok" {
		t.Fatalf("events = %+v", events)
	}
}

func TestParseStopsEarly(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "plain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	if err := Parse(f, func(Event) bool { n++; return n < 2 }); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("emit called %d times after returning false, want 2", n)
	}
}
