package guard

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func genIntegrityInput(r *rand.Rand) IntegrityInput {
	turn := pick(r, TurnID("t1"), TurnID("t1"), TurnID("t1"), TurnID("t2"))
	phase := pick(r, PhaseBefore, PhaseAfter)
	snap := pick(r, Snapshot{Head: "a"}, Snapshot{Head: "a"}, Snapshot{Head: "b"}, Snapshot{Head: "a", Paths: []string{"x"}}, Snapshot{Head: "a", Status: "z", Paths: []string{"y"}})
	switch r.Intn(5) {
	case 0:
		return TurnStarts{Turn: turn, Mode: pick(r, allModes...)}
	case 1, 2:
		return SnapshotTaken{Turn: turn, Phase: phase, Snap: snap}
	case 3:
		return SnapshotFailed{Turn: turn, Phase: phase, Reason: "boom"}
	default:
		return TurnEnds{Turn: turn}
	}
}

// guided picks the input that moves the flow forward half of the time, so
// runs reach the later states.
func guidedIntegrityInput(r *rand.Rand, s Integrity) IntegrityInput {
	if r.Intn(2) == 0 {
		return genIntegrityInput(r)
	}
	switch st := s.(type) {
	case IntegrityIdle:
		return TurnStarts{Turn: "t1", Mode: core.ModeAsk}
	case Arming:
		return SnapshotTaken{Turn: st.Turn, Phase: PhaseBefore, Snap: Snapshot{Head: "a"}}
	case Armed:
		return TurnEnds{Turn: st.Turn}
	case Checking:
		return SnapshotTaken{Turn: st.Turn, Phase: PhaseAfter, Snap: pick(r, Snapshot{Head: "a"}, Snapshot{Head: "b"}, st.Before)}
	}
	return genIntegrityInput(r)
}

func driveIntegrity(t *testing.T, chk func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command)) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		var s Integrity = IntegrityIdle{}
		for i := 0; i < 30; i++ {
			in := guidedIntegrityInput(r, s)
			next, cmds := StepIntegrity(s, in)
			chk(s, in, next, cmds)
			s = next
		}
	})
}

func isTerminal(s Integrity) bool {
	switch s.(type) {
	case Clean, Violated, Unverifiable:
		return true
	}
	return false
}

func hasStop(cmds []Command) bool {
	for _, c := range cmds {
		if _, ok := c.(StopTask); ok {
			return true
		}
	}
	return false
}

// I1
func TestPropIntegrityStopsOnChange(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		switch next.(type) {
		case Violated, Unverifiable:
			if !isTerminal(prev) && !hasStop(cmds) {
				t.Fatalf("%#v -> %#v without a StopTask: %#v", prev, next, cmds)
			}
		case Clean:
			if hasStop(cmds) {
				t.Fatalf("Clean with a StopTask: %#v", cmds)
			}
			for _, c := range cmds {
				if _, ok := c.(FlagTask); ok {
					t.Fatalf("Clean with a FlagTask: %#v", cmds)
				}
			}
		}
		if reflect.DeepEqual(prev, next) && len(cmds) != 0 {
			t.Fatalf("no change but commands %#v", cmds)
		}
	})
}

// I2
func TestPropIntegrityEqualIffClean(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		ck, ok := prev.(Checking)
		st, ok2 := in.(SnapshotTaken)
		if !ok || !ok2 || st.Turn != ck.Turn || st.Phase != PhaseAfter {
			return
		}
		_, clean := next.(Clean)
		_, viol := next.(Violated)
		if ck.Before.Equal(st.Snap) != clean || ck.Before.Equal(st.Snap) == viol {
			t.Fatalf("before %+v after %+v -> %#v", ck.Before, st.Snap, next)
		}
	})
}

// I3
func TestPropIntegrityTerminal(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		if isTerminal(prev) && (!reflect.DeepEqual(prev, next) || len(cmds) != 0) {
			t.Fatalf("terminal %#v changed by %#v to %#v with %#v", prev, in, next, cmds)
		}
	})
}

// I4: the integrity step only emits commands that read the tree, stop the
// task, flag it or audit; no command can write to or revert a working tree.
func TestIntegrityCommandsAreReadOnly(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		for _, c := range cmds {
			switch c.(type) {
			case TakeSnapshot, StopTask, FlagTask, Audit:
			default:
				t.Fatalf("unexpected command %T", c)
			}
		}
	})
}

// A turn only ever moves forward through the states.
func TestPropIntegrityTurnIDKept(t *testing.T) {
	turnOf := func(s Integrity) TurnID {
		switch st := s.(type) {
		case Arming:
			return st.Turn
		case Armed:
			return st.Turn
		case Checking:
			return st.Turn
		case Clean:
			return st.Turn
		case Violated:
			return st.Turn
		case Unverifiable:
			return st.Turn
		}
		return ""
	}
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		if _, idle := prev.(IntegrityIdle); !idle && turnOf(next) != turnOf(prev) {
			t.Fatalf("turn changed from %q to %q", turnOf(prev), turnOf(next))
		}
	})
}
