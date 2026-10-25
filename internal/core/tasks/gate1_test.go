package tasks

import (
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// K9, fixed cases: the boundary of each cap is checked by value, so the
// formula in TestPropSubagentCapsEnforced cannot agree with a wrong step by
// repeating the same mistake.
func TestSubagentCapTable(t *testing.T) {
	caps := Caps{Depth: 2, Concurrent: 2}
	two := []Subagent{{ID: "a", Depth: 1}, {ID: "b", Depth: 1}}
	one := two[:1]
	cases := []struct {
		name     string
		active   []Subagent
		in       SubagentStarted
		wantKill bool
	}{
		{"depth equals cap", nil, SubagentStarted{AgentID: "x", Depth: 2}, false},
		{"depth over cap", nil, SubagentStarted{AgentID: "x", Depth: 3}, true},
		{"concurrent below cap", one, SubagentStarted{AgentID: "x", Depth: 1}, false},
		{"concurrent equals cap", two, SubagentStarted{AgentID: "x", Depth: 1}, true},
		{"repeated id at cap", two, SubagentStarted{AgentID: "a", Depth: 1}, false},
		{"repeated id too deep", two, SubagentStarted{AgentID: "a", Depth: 3}, true},
		{"zero depth is not over", nil, SubagentStarted{AgentID: "x", Depth: 0}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.Turn, c.in.Attempt = "t1", 1
			tk := Task{ID: "k", Mode: core.ModeCode, Caps: caps, Turns: 1,
				State: Running{Turn: "t1", Attempt: 1, Subagents: append([]Subagent(nil), c.active...)}}
			got, cmds := Step(tk, c.in)
			if hasCmd[KillRun](cmds) != c.wantKill {
				t.Fatalf("KillRun = %v, want %v", hasCmd[KillRun](cmds), c.wantKill)
			}
			st, stopping := got.State.(Stopping)
			if c.wantKill && (!stopping || st.Cause != CauseCapExceeded) {
				t.Fatalf("state %#v, want Stopping{CapExceeded}", got.State)
			}
			if !c.wantKill {
				r, ok := got.State.(Running)
				if !ok {
					t.Fatalf("state %#v, want Running", got.State)
				}
				if c.in.AgentID == "a" && len(r.Subagents) != len(c.active) {
					t.Fatalf("a repeated agent ID changed the active set: %v", r.Subagents)
				}
			}
		})
	}
}

// K15, positive side: known inputs that must suggest raising.
func TestRoomHintSuggestsWhenThereIsRoom(t *testing.T) {
	cases := []struct {
		cpus, mem, max, active, want int
	}{
		{8, 4096, 1, 1, 2},
		{12, 6144, 2, 2, 3},
		{32, 20000, 7, 7, 8},
		{32, 20000, 3, 5, 4}, // more active than the limit still counts as full
	}
	for _, c := range cases {
		if got := RoomHint(c.cpus, c.mem, c.max, c.active); got.RaiseTo != c.want {
			t.Errorf("RoomHint(%d,%d,%d,%d) = %d, want %d", c.cpus, c.mem, c.max, c.active, got.RaiseTo, c.want)
		}
	}
	// One short of the room on either axis suggests nothing.
	for _, c := range []struct{ cpus, mem int }{{7, 4096}, {8, 4095}} {
		if got := RoomHint(c.cpus, c.mem, 1, 1); got.RaiseTo != 0 {
			t.Errorf("RoomHint(%d,%d,1,1) = %d, want none", c.cpus, c.mem, got.RaiseTo)
		}
	}
}

// K15 as an equivalence: RoomHint suggests max+1 exactly when the slots are
// full, max+1 is within the ceiling and the machine has room for max+1.
func TestPropRoomHintSuggestsWhenRoom(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		cpus, mem, mx, active := r.Intn(80), r.Intn(100000), r.Intn(10)+1, r.Intn(12)
		want := 0
		if active >= mx && mx+1 <= RoomHintCeiling && cpus >= 4*(mx+1) && mem >= 2048*(mx+1) {
			want = mx + 1
		}
		if got := RoomHint(cpus, mem, mx, active); got.RaiseTo != want {
			t.Fatalf("RoomHint(%d,%d,%d,%d) = %d, want %d", cpus, mem, mx, active, got.RaiseTo, want)
		}
	})
}
