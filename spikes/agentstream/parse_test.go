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
