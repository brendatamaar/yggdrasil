package claudecode

import (
	"slices"
	"testing"
)

func toolUseLine(id string) []byte {
	return []byte(`{"type":"assistant","message":{"id":"m_` + id + `","role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Read","input":{"file_path":"a.go"}}]}}`)
}

func TestFullSeenListCountsAsUnhooked(t *testing.T) {
	p := newParser(t.TempDir())
	p.maxSeen = 2
	for _, id := range []string{"t1", "t2", "t3"} {
		p.parseLine(toolUseLine(id))
	}
	// Every kept ID has a hook record, yet the dropped third cannot be checked.
	got := p.unhookedToolUses([]string{"t1", "t2", "t3"})
	if !slices.Contains(got, seenListFull) {
		t.Fatalf("unhooked = %q, want the %q sentinel", got, seenListFull)
	}
}

func TestSeenListAtTheCapIsNotFull(t *testing.T) {
	p := newParser(t.TempDir())
	p.maxSeen = 2
	for _, id := range []string{"t1", "t2"} {
		p.parseLine(toolUseLine(id))
	}
	if got := p.unhookedToolUses([]string{"t1", "t2"}); len(got) != 0 {
		t.Fatalf("unhooked = %q, want none", got)
	}
}
