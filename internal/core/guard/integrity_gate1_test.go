package guard

import (
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// M2b: a snapshot with an empty field proves nothing, so it is Unverifiable.
func TestIntegrityIncompleteSnapshotIsUnverifiable(t *testing.T) {
	const turn = TurnID("t1")
	for name, snap := range map[string]Snapshot{
		"empty head":   {Status: "s", DiffHash: "d"},
		"empty status": {Head: "h", DiffHash: "d"},
		"empty diff":   {Head: "h", Status: "s"},
		"all empty":    {},
	} {
		t.Run("before/"+name, func(t *testing.T) {
			got, cmds := StepIntegrity(Arming{Turn: turn}, SnapshotTaken{Turn: turn, Phase: PhaseBefore, Snap: snap})
			if _, ok := got.(Unverifiable); !ok {
				t.Fatalf("got %#v", got)
			}
			wantStop(t, cmds, turn, StopCannotVerify)
		})
		t.Run("after/"+name, func(t *testing.T) {
			// Two identical empty-field snapshots must not count as Clean.
			got, cmds := StepIntegrity(Checking{Turn: turn, Before: snap}, SnapshotTaken{Turn: turn, Phase: PhaseAfter, Snap: snap})
			if _, ok := got.(Unverifiable); !ok {
				t.Fatalf("got %#v", got)
			}
			wantStop(t, cmds, turn, StopCannotVerify)
		})
		t.Run("after only/"+name, func(t *testing.T) {
			got, _ := StepIntegrity(Checking{Turn: turn, Before: snapA}, SnapshotTaken{Turn: turn, Phase: PhaseAfter, Snap: snap})
			if _, ok := got.(Unverifiable); !ok {
				t.Fatalf("got %#v", got)
			}
		})
	}
}

// M2c: a cancelled or crashed turn.
func TestIntegrityAbort(t *testing.T) {
	const turn = TurnID("t1")
	cases := []struct {
		name string
		from Integrity
		in   IntegrityInput
		to   Integrity
	}{
		{"arming", Arming{Turn: turn}, TurnAborted{Turn: turn}, IntegrityIdle{}},
		{"armed", Armed{Turn: turn, Before: snapA}, TurnAborted{Turn: turn}, Unverifiable{Turn: turn, Why: "the turn was cancelled or crashed before it could be checked"}},
		{"checking", Checking{Turn: turn, Before: snapA}, TurnAborted{Turn: turn}, Unverifiable{Turn: turn, Why: "the turn was cancelled or crashed before it could be checked"}},
		{"other turn, arming", Arming{Turn: turn}, TurnAborted{Turn: "t2"}, Arming{Turn: turn}},
		{"other turn, armed", Armed{Turn: turn, Before: snapA}, TurnAborted{Turn: "t2"}, Armed{Turn: turn, Before: snapA}},
		{"idle", IntegrityIdle{}, TurnAborted{Turn: turn}, IntegrityIdle{}},
		{"clean", Clean{Turn: turn}, TurnAborted{Turn: turn}, Clean{Turn: turn}},
		{"violated", Violated{Turn: turn}, TurnAborted{Turn: turn}, Violated{Turn: turn}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, cmds := StepIntegrity(c.from, c.in)
			if !reflect.DeepEqual(got, c.to) {
				t.Fatalf("state %#v, want %#v", got, c.to)
			}
			if hasStop(cmds) {
				t.Fatalf("abort emitted a StopTask: %#v", cmds)
			}
			if _, un := got.(Unverifiable); un && !reflect.DeepEqual(c.from, got) {
				if len(cmds) != 1 {
					t.Fatalf("want one audit entry, got %#v", cmds)
				}
				if a, ok := cmds[0].(Audit); !ok || a.Entry.Outcome != "unverifiable" || a.Entry.Turn != turn {
					t.Fatalf("got %#v", cmds)
				}
			}
		})
	}
}

// Restart: a NEW turn ID starts fresh from every terminal state.
func TestIntegrityRestartFromTerminalStates(t *testing.T) {
	for name, from := range map[string]Integrity{
		"clean":        Clean{Turn: "t1"},
		"violated":     Violated{Turn: "t1", Changes: []Change{{Path: "a"}}},
		"unverifiable": Unverifiable{Turn: "t1", Why: "x"},
	} {
		t.Run(name+"/ask", func(t *testing.T) {
			got, cmds := StepIntegrity(from, TurnStarts{Turn: "t2", Mode: core.ModeAsk})
			if !reflect.DeepEqual(got, Arming{Turn: "t2"}) || !reflect.DeepEqual(cmds, []Command{TakeSnapshot{Turn: "t2", Phase: PhaseBefore}}) {
				t.Fatalf("got %#v %#v", got, cmds)
			}
		})
		t.Run(name+"/code", func(t *testing.T) {
			got, cmds := StepIntegrity(from, TurnStarts{Turn: "t2", Mode: core.ModeCode})
			if !reflect.DeepEqual(got, IntegrityIdle{}) || len(cmds) != 0 {
				t.Fatalf("got %#v %#v", got, cmds)
			}
		})
		t.Run(name+"/same turn", func(t *testing.T) {
			got, cmds := StepIntegrity(from, TurnStarts{Turn: "t1", Mode: core.ModeAsk})
			if !reflect.DeepEqual(got, from) || len(cmds) != 0 {
				t.Fatalf("got %#v %#v", got, cmds)
			}
		})
	}
	// A live check is never replaced by a different turn.
	live := Armed{Turn: "t1", Before: snapA}
	if got, cmds := StepIntegrity(live, TurnStarts{Turn: "t2", Mode: core.ModeAsk}); !reflect.DeepEqual(got, live) || len(cmds) != 0 {
		t.Fatalf("got %#v %#v", got, cmds)
	}
}
