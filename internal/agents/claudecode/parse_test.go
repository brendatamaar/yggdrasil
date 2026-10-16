package claudecode

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/agents"
)

// parseFixture feeds a recorded stream to a parser and returns every event
// with a Seq, as the runner would assign.
func parseFixture(t *testing.T, name, dir string) ([]agents.Event, *parser) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p := newParser(dir)
	var events []agents.Event
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		events = append(events, p.parseLine(line)...)
	}
	for i := range events {
		events[i].Seq = int64(i + 1)
	}
	return events, p
}

func kinds(events []agents.Event, k agents.EventKind) []agents.Event {
	var out []agents.Event
	for _, e := range events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func sumUsage(events []agents.Event) agents.Usage {
	var u agents.Usage
	for _, e := range kinds(events, agents.EventUsage) {
		u.InputTokens += e.Usage.InputTokens
		u.OutputTokens += e.Usage.OutputTokens
		u.CacheWriteTokens += e.Usage.CacheWriteTokens
		u.CacheReadTokens += e.Usage.CacheReadTokens
	}
	return u
}

func TestParseLiveSubagent(t *testing.T) {
	events, p := parseFixture(t, "live-subagent.jsonl", "")

	starts := kinds(events, agents.EventSubagentStart)
	if len(starts) != 1 || starts[0].ID != "toolu_scrub0001" || starts[0].ParentID != "" || starts[0].Text != "Read README first line" {
		t.Fatalf("sub-agent start = %+v", starts)
	}
	ends := kinds(events, agents.EventSubagentEnd)
	if len(ends) != 1 || ends[0].ParentID != "toolu_scrub0001" || ends[0].AgentID != "a0000000000000001" {
		t.Fatalf("sub-agent end = %+v", ends)
	}
	// Everything the sub-agent did hangs under its start event.
	inside := 0
	for _, e := range events {
		if e.AgentID == "a0000000000000001" && e.Kind != agents.EventSubagentEnd {
			inside++
			if e.ParentID != "toolu_scrub0001" {
				t.Errorf("sub-agent event %s has parent %q", e.Kind, e.ParentID)
			}
		}
	}
	if inside < 4 {
		t.Errorf("only %d events carry the sub-agent's ID", inside)
	}
	// The hand-back of the sub-agent belongs to the main agent.
	for _, e := range kinds(events, agents.EventToolResult) {
		if e.ResultFor == "toolu_scrub0001" && e.ParentID != "" {
			t.Errorf("sub-agent result has parent %q, want the main agent", e.ParentID)
		}
	}

	// Tokens are counted once per message, sub-agent included: the stream
	// repeats a message once per content block.
	u := sumUsage(events)
	if u.InputTokens != 8 || u.OutputTokens != 29 || u.CacheWriteTokens != 21159 || u.CacheReadTokens != 64524 {
		t.Errorf("token totals = %+v", u)
	}
	final := events[len(events)-1]
	if final.Kind != agents.EventUsage || final.Usage.CostUSD == nil || *final.Usage.CostUSD < 0.00442 || *final.Usage.CostUSD > 0.00443 ||
		final.Usage.DurationMS != 9930 || final.Usage.Turns != 2 || final.Usage.InputTokens != 0 || final.Usage.Cumulative {
		t.Errorf("final usage = %+v", final.Usage)
	}
	if p.session != "00000000-0000-4000-8000-000000000001" || !p.sawResult || !p.resultOK {
		t.Errorf("session %q, sawResult %v, ok %v", p.session, p.sawResult, p.resultOK)
	}
	for _, e := range kinds(events, agents.EventMessage) {
		if strings.TrimSpace(e.Text) == "" {
			t.Errorf("empty message event (a thinking block leaked?): %+v", e)
		}
	}
}

// Account data in the stream (init, command list, rate limits, signatures)
// must never reach an event.
func TestParseKeepsNoAccountData(t *testing.T) {
	for _, name := range []string{"live-subagent.jsonl", "live-permission-denied.jsonl"} {
		events, _ := parseFixture(t, name, "")
		raw, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{
			"mcp__", "powershell", "apiKeySource", "slash_commands", "rate_limit", "resetsAt", "five_hour",
			"SCRUBBED-SIGNATURE", "claude_code_version", "claude-haiku", "permissionMode", "tool_result_meta", "wire_tool_inputs",
		} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s: events contain %q", name, leak)
			}
		}
	}
}

func TestParsePermissionDenied(t *testing.T) {
	events, _ := parseFixture(t, "live-permission-denied.jsonl", "")
	errs := kinds(events, agents.EventError)
	if len(errs) != 1 {
		t.Fatalf("error events = %+v", errs)
	}
	e := errs[0]
	if e.ResultFor != "toolu_scrub0002" || e.ParentID != "toolu_scrub0001" || e.AgentID != "a0000000000000001" ||
		!strings.Contains(e.Error, "Read") || strings.Contains(e.Error, "don't ask") {
		t.Errorf("permission error = %+v", e)
	}
	// The sub-agent's own denied tool result is still shown.
	var denied bool
	for _, r := range kinds(events, agents.EventToolResult) {
		denied = denied || (r.ResultFor == "toolu_scrub0002" && r.IsError)
	}
	if !denied {
		t.Error("the denied tool result is missing")
	}
}

func TestParseNestedTwoLevels(t *testing.T) {
	events, _ := parseFixture(t, "nested.jsonl", "")
	roots := agents.BuildTree(events)
	var a *agents.Node
	for _, r := range roots {
		if r.Orphan {
			t.Errorf("orphan root %+v", r.Event)
		}
		if r.Event.ID == "toolu_suba" {
			a = r
		}
	}
	if a == nil || a.Event.Kind != agents.EventSubagentStart {
		t.Fatalf("no root for the first sub-agent: %+v", roots)
	}
	var b *agents.Node
	for _, c := range a.Children {
		if c.Event.ID == "toolu_subb" {
			b = c
		}
	}
	if b == nil || b.Event.Kind != agents.EventSubagentStart {
		t.Fatalf("the second sub-agent is not a child of the first: %+v", a.Children)
	}
	var call bool
	for _, c := range b.Children {
		call = call || (c.Event.ID == "toolu_subc" && c.Event.Kind == agents.EventToolCall && c.Event.Tool.Name == "Read")
	}
	if !call {
		t.Errorf("the Read call is not under the second sub-agent: %+v", b.Children)
	}
	if got := len(kinds(events, agents.EventSubagentEnd)); got != 2 {
		t.Errorf("sub-agent end events = %d, want 2", got)
	}
	// "Task" is the older name of the sub-agent tool and is still understood.
	if starts := kinds(events, agents.EventSubagentStart); len(starts) != 2 || starts[0].Tool.Name != "Task" {
		t.Errorf("starts = %+v", starts)
	}
}

func TestParseEditsAndCommands(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("the fixture uses Windows paths")
	}
	events, p := parseFixture(t, "edits.jsonl", `C:\work\shop-api`)

	edits := kinds(events, agents.EventFileEdit)
	if len(edits) != 2 {
		t.Fatalf("file edits = %+v", edits)
	}
	if e := edits[0].File; e.Path != "src/cart.js" || e.Op != "modify" || !strings.Contains(e.Diff, "-return a - b;") || !strings.Contains(e.Diff, "+return a + b;") {
		t.Errorf("edit = %+v", e)
	}
	// A path outside the worktree stays visible in full, never made relative.
	if e := edits[1].File; e.Path != "C:/elsewhere/notes.txt" || e.Op != "create" {
		t.Errorf("write = %+v", e)
	}
	cmds := kinds(events, agents.EventCommand)
	if len(cmds) != 1 || cmds[0].Command.Line != "npm test" || cmds[0].ID != "toolu_e3" {
		t.Errorf("commands = %+v", cmds)
	}
	var failedResult bool
	for _, r := range kinds(events, agents.EventToolResult) {
		failedResult = failedResult || (r.ResultFor == "toolu_e3" && r.IsError && r.Text == "1 test failed")
	}
	if !failedResult {
		t.Error("the failing command result is missing")
	}
	errs := kinds(events, agents.EventError)
	if len(errs) != 1 || !strings.Contains(errs[0].Error, "turn limit") {
		t.Errorf("result error = %+v", errs)
	}
	if !p.sawResult || p.resultOK {
		t.Errorf("sawResult %v, ok %v: want a finished but failed run", p.sawResult, p.resultOK)
	}
}

func TestParseMalformedAndUnknown(t *testing.T) {
	events, p := parseFixture(t, "malformed.jsonl", "")
	errs := kinds(events, agents.EventError)
	if len(errs) != 3 { // cut-off JSON, plain text, content that is neither string nor blocks
		t.Fatalf("error events = %d: %+v", len(errs), errs)
	}
	raw, _ := json.Marshal(events)
	for _, leak := range []string{"cut off", "not json at all", "brand_new", "future_block"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("events contain raw stream text %q", leak)
		}
	}
	msgs := kinds(events, agents.EventMessage)
	if len(msgs) != 1 || msgs[0].Text != "Still going." {
		t.Errorf("messages = %+v", msgs)
	}
	if !p.resultOK {
		t.Error("the result line after the bad lines was lost")
	}
}

func TestParseCapsErrorEvents(t *testing.T) {
	p := newParser("")
	n := 0
	for range 100 {
		n += len(p.parseLine([]byte("not json")))
	}
	if n != maxBadLines {
		t.Errorf("error events = %d, want %d", n, maxBadLines)
	}
	if got := p.parseLine([]byte("   \r\n")); got != nil {
		t.Errorf("blank line gave %+v", got)
	}
}

func TestCapText(t *testing.T) {
	long := strings.Repeat("é", maxTextBytes) // two bytes per rune
	got := capText(long)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "[truncated]") || len(got) > maxTextBytes+20 {
		t.Errorf("capText produced %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if short := "small"; capText(short) != short {
		t.Error("short text changed")
	}
	big := json.RawMessage(`{"x":"` + strings.Repeat("a", maxTextBytes) + `"}`)
	if got := capInput(big); string(got) != `{"truncated":true}` {
		t.Errorf("capInput = %s", got)
	}
	if got := safeName("Rea\x00d\n"); got != "Read" {
		t.Errorf("safeName = %q", got)
	}
}

func TestRelPath(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("Windows paths")
	}
	p := newParser(`C:\work\shop-api`)
	for in, want := range map[string]string{
		`C:\work\shop-api\src\a.go`: "src/a.go",
		`src\b.go`:                  "src/b.go",
		`C:\work\elsewhere\c.go`:    "C:/work/elsewhere/c.go",
		`C:\work\shop-apix\d.go`:    "C:/work/shop-apix/d.go",
		``:                          "",
	} {
		if got := p.relPath(in); got != want {
			t.Errorf("relPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEditDiff(t *testing.T) {
	if editDiff("", "") != "" {
		t.Error("an empty edit has a diff")
	}
	if got := editDiff("a\nb", "c"); got != "@@ edit @@\n-a\n-b\n+c\n" {
		t.Errorf("editDiff = %q", got)
	}
}
