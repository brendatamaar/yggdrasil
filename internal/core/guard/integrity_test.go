package guard

import (
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var (
	snapA = Snapshot{Head: "h1", Status: "s1", DiffHash: "d1"}
	snapB = Snapshot{Head: "h1", Status: "s2", DiffHash: "d1", Paths: []string{"src/a.go", "new.txt"}}
)

func TestStepIntegrityTable(t *testing.T) {
	const turn = TurnID("t1")
	cases := []struct {
		name  string
		from  Integrity
		in    IntegrityInput
		to    Integrity
		cmds  []Command
		check func(t *testing.T, cmds []Command)
	}{
		{"idle + ask starts", IntegrityIdle{}, TurnStarts{Turn: turn, Mode: core.ModeAsk}, Arming{Turn: turn},
			[]Command{TakeSnapshot{Turn: turn, Phase: PhaseBefore}}, nil},
		{"idle + code starts: no check", IntegrityIdle{}, TurnStarts{Turn: turn, Mode: core.ModeCode}, IntegrityIdle{}, nil, nil},
		{"idle + project starts: no check", IntegrityIdle{}, TurnStarts{Turn: turn, Mode: core.ModeProject}, IntegrityIdle{}, nil, nil},
		{"arming + before taken", Arming{Turn: turn}, SnapshotTaken{Turn: turn, Phase: PhaseBefore, Snap: snapA}, Armed{Turn: turn, Before: snapA}, nil, nil},
		{"arming + before failed", Arming{Turn: turn}, SnapshotFailed{Turn: turn, Phase: PhaseBefore, Reason: "locked"},
			Unverifiable{Turn: turn, Why: "locked"}, nil, func(t *testing.T, c []Command) { wantStop(t, c, turn, StopCannotVerify) }},
		{"arming + other turn", Arming{Turn: turn}, SnapshotTaken{Turn: "t2", Phase: PhaseBefore, Snap: snapA}, Arming{Turn: turn}, nil, nil},
		{"arming + wrong phase", Arming{Turn: turn}, SnapshotTaken{Turn: turn, Phase: PhaseAfter, Snap: snapA}, Arming{Turn: turn}, nil, nil},
		{"arming + turn ends", Arming{Turn: turn}, TurnEnds{Turn: turn}, Unverifiable{Turn: turn, Why: "the turn ended before the snapshot before it was taken"}, nil, func(t *testing.T, c []Command) { wantStop(t, c, turn, StopCannotVerify) }},
		{"armed + turn ends", Armed{Turn: turn, Before: snapA}, TurnEnds{Turn: turn}, Checking{Turn: turn, Before: snapA},
			[]Command{TakeSnapshot{Turn: turn, Phase: PhaseAfter}}, nil},
		{"armed + other turn ends", Armed{Turn: turn, Before: snapA}, TurnEnds{Turn: "t2"}, Armed{Turn: turn, Before: snapA}, nil, nil},
		{"armed + stray snapshot", Armed{Turn: turn, Before: snapA}, SnapshotTaken{Turn: turn, Phase: PhaseAfter, Snap: snapB}, Armed{Turn: turn, Before: snapA}, nil, nil},
		{"checking + equal", Checking{Turn: turn, Before: snapA}, SnapshotTaken{Turn: turn, Phase: PhaseAfter, Snap: Snapshot{Head: "h1", Status: "s1", DiffHash: "d1", Paths: []string{"ignored"}}},
			Clean{Turn: turn}, nil, func(t *testing.T, c []Command) {
				if len(c) != 1 {
					t.Fatalf("clean emitted %#v", c)
				}
				if _, ok := c[0].(Audit); !ok {
					t.Fatalf("clean emitted %#v", c)
				}
			}},
		{"checking + changed", Checking{Turn: turn, Before: snapA}, SnapshotTaken{Turn: turn, Phase: PhaseAfter, Snap: snapB},
			Violated{Turn: turn, Changes: []Change{{Path: "src/a.go"}, {Path: "new.txt"}}}, nil, func(t *testing.T, c []Command) {
				wantStop(t, c, turn, StopTreeChanged)
				var flagged bool
				for _, cmd := range c {
					if f, ok := cmd.(FlagTask); ok && f.Turn == turn && len(f.Changes) == 2 {
						flagged = true
					}
				}
				if !flagged {
					t.Fatalf("no FlagTask in %#v", c)
				}
			}},
		{"checking + changed, no paths known", Checking{Turn: turn, Before: snapA}, SnapshotTaken{Turn: turn, Phase: PhaseAfter, Snap: Snapshot{Head: "h2", Status: "s1", DiffHash: "d1"}},
			Violated{Turn: turn}, nil, func(t *testing.T, c []Command) { wantStop(t, c, turn, StopTreeChanged) }},
		{"checking + after failed", Checking{Turn: turn, Before: snapA}, SnapshotFailed{Turn: turn, Phase: PhaseAfter, Reason: "git missing"},
			Unverifiable{Turn: turn, Why: "git missing"}, nil, func(t *testing.T, c []Command) { wantStop(t, c, turn, StopCannotVerify) }},
		{"checking + other turn", Checking{Turn: turn, Before: snapA}, SnapshotTaken{Turn: "t2", Phase: PhaseAfter, Snap: snapB}, Checking{Turn: turn, Before: snapA}, nil, nil},
		{"checking + before phase", Checking{Turn: turn, Before: snapA}, SnapshotTaken{Turn: turn, Phase: PhaseBefore, Snap: snapB}, Checking{Turn: turn, Before: snapA}, nil, nil},
		{"clean is terminal", Clean{Turn: turn}, TurnEnds{Turn: turn}, Clean{Turn: turn}, nil, nil},
		{"violated is terminal", Violated{Turn: turn}, SnapshotTaken{Turn: turn, Phase: PhaseAfter, Snap: snapA}, Violated{Turn: turn}, nil, nil},
		{"unverifiable is terminal", Unverifiable{Turn: turn}, TurnStarts{Turn: turn, Mode: core.ModeAsk}, Unverifiable{Turn: turn}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, cmds := StepIntegrity(c.from, c.in)
			if !reflect.DeepEqual(got, c.to) {
				t.Fatalf("state %#v, want %#v", got, c.to)
			}
			if c.cmds != nil || c.check == nil {
				if !reflect.DeepEqual(cmds, c.cmds) {
					t.Fatalf("commands %#v, want %#v", cmds, c.cmds)
				}
			}
			if c.check != nil {
				c.check(t, cmds)
			}
		})
	}
}

func wantStop(t *testing.T, cmds []Command, turn TurnID, why StopReason) {
	t.Helper()
	for _, c := range cmds {
		if s, ok := c.(StopTask); ok && s.Turn == turn && s.Reason == why {
			return
		}
	}
	t.Fatalf("no StopTask{%s,%s} in %#v", turn, why, cmds)
}

// The whole Ask flow on a clean tree and on a changed one.
func TestIntegrityFlow(t *testing.T) {
	run := func(after Snapshot) Integrity {
		var s Integrity = IntegrityIdle{}
		for _, in := range []IntegrityInput{
			TurnStarts{Turn: "t", Mode: core.ModeAsk},
			SnapshotTaken{Turn: "t", Phase: PhaseBefore, Snap: snapA},
			TurnEnds{Turn: "t"},
			SnapshotTaken{Turn: "t", Phase: PhaseAfter, Snap: after},
		} {
			s, _ = StepIntegrity(s, in)
		}
		return s
	}
	if _, ok := run(snapA).(Clean); !ok {
		t.Error("unchanged tree was not Clean")
	}
	if _, ok := run(snapB).(Violated); !ok {
		t.Error("changed tree was not Violated")
	}
}
