package guard

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// snapOf builds a complete snapshot (all three fields set) from a seed.
func snapOf(seed string) Snapshot {
	return Snapshot{Head: "h" + seed, Status: "s" + seed, DiffHash: "d" + seed}
}

func withPaths(s Snapshot, paths ...string) Snapshot {
	s.Paths = paths
	return s
}

func genIntegrityInput(r *rand.Rand) IntegrityInput {
	turn := pick(r, TurnID("t1"), TurnID("t1"), TurnID("t1"), TurnID("t2"), TurnID("t3"))
	phase := pick(r, PhaseBefore, PhaseAfter)
	snap := pick(r, snapOf("a"), snapOf("a"), snapOf("b"), withPaths(snapOf("a"), "x"), withPaths(snapOf("a2"), "y"),
		Snapshot{Head: "a"}, Snapshot{Head: "a", Status: "s"}, Snapshot{})
	switch r.Intn(6) {
	case 0:
		return TurnStarts{Turn: turn, Mode: pick(r, allModes...)}
	case 1, 2:
		return SnapshotTaken{Turn: turn, Phase: phase, Snap: snap}
	case 3:
		return SnapshotFailed{Turn: turn, Phase: phase, Reason: "boom"}
	case 4:
		return TurnAborted{Turn: turn}
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
		return SnapshotTaken{Turn: st.Turn, Phase: PhaseBefore, Snap: snapOf("a")}
	case Armed:
		return TurnEnds{Turn: st.Turn}
	case Checking:
		return SnapshotTaken{Turn: st.Turn, Phase: PhaseAfter, Snap: pick(r, snapOf("a"), snapOf("b"), st.Before)}
	case Clean, Violated, Unverifiable:
		return TurnStarts{Turn: pick(r, TurnID("t2"), TurnID("t3")), Mode: pick(r, allModes...)}
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

func turnOfIntegrity(s Integrity) TurnID {
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

// I1
func TestPropIntegrityStopsOnChange(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		_, aborted := in.(TurnAborted)
		switch next.(type) {
		case Violated, Unverifiable:
			if !isTerminal(prev) && !hasStop(cmds) && !aborted {
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
		if aborted && hasStop(cmds) {
			t.Fatalf("an aborted turn is already stopped, but got %#v", cmds)
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
		complete := snapComplete(st.Snap)
		if clean != (complete && ck.Before.Equal(st.Snap)) || viol != (complete && !ck.Before.Equal(st.Snap)) {
			t.Fatalf("before %+v after %+v -> %#v", ck.Before, st.Snap, next)
		}
		if !complete {
			if _, un := next.(Unverifiable); !un {
				t.Fatalf("an incomplete snapshot %+v gave %#v", st.Snap, next)
			}
		}
	})
}

func snapComplete(s Snapshot) bool { return s.Head != "" && s.Status != "" && s.DiffHash != "" }

// I2b: an incomplete Before snapshot can never arm the check.
func TestPropIntegrityNeverArmedOnIncompleteSnapshot(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		if a, ok := next.(Armed); ok && !snapComplete(a.Before) {
			t.Fatalf("armed with %+v", a.Before)
		}
		if _, was := prev.(Arming); was {
			if st, ok := in.(SnapshotTaken); ok && !snapComplete(st.Snap) {
				if _, armed := next.(Armed); armed {
					t.Fatalf("armed by %+v", st.Snap)
				}
			}
		}
	})
}

// I3: Clean, Violated and Unverifiable change only when a NEW turn starts;
// then the check starts fresh (Ask) or stays out of the way (other modes).
func TestPropIntegrityTerminal(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		if !isTerminal(prev) {
			return
		}
		if ts, ok := in.(TurnStarts); ok && ts.Turn != turnOfIntegrity(prev) {
			_, arming := next.(Arming)
			_, idle := next.(IntegrityIdle)
			if (ts.Mode == core.ModeAsk) != arming || (ts.Mode != core.ModeAsk) != idle {
				t.Fatalf("a new turn %#v after %#v gave %#v", ts, prev, next)
			}
			return
		}
		if !reflect.DeepEqual(prev, next) || len(cmds) != 0 {
			t.Fatalf("terminal %#v changed by %#v to %#v with %#v", prev, in, next, cmds)
		}
	})
}

// I5: an abort of the current turn leaves no live state behind: Arming goes
// back to Idle, Armed and Checking become Unverifiable. An abort for another
// turn changes nothing.
func TestPropIntegrityAbort(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		ab, ok := in.(TurnAborted)
		if !ok {
			return
		}
		live := prev
		switch prev.(type) {
		case Arming, Armed, Checking:
		default:
			if !reflect.DeepEqual(prev, next) || len(cmds) != 0 {
				t.Fatalf("abort changed %#v to %#v", prev, next)
			}
			return
		}
		if ab.Turn != turnOfIntegrity(live) {
			if !reflect.DeepEqual(prev, next) || len(cmds) != 0 {
				t.Fatalf("abort of another turn changed %#v to %#v", prev, next)
			}
			return
		}
		switch prev.(type) {
		case Arming:
			if _, idle := next.(IntegrityIdle); !idle {
				t.Fatalf("aborted Arming gave %#v", next)
			}
		default:
			if _, un := next.(Unverifiable); !un {
				t.Fatalf("aborted %#v gave %#v", prev, next)
			}
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

// A turn keeps its ID through the states; only Idle and a fresh start after a
// terminal state pick a new one.
func TestPropIntegrityTurnIDKept(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		if _, idle := prev.(IntegrityIdle); idle {
			return
		}
		if _, ok := in.(TurnStarts); ok && isTerminal(prev) {
			return
		}
		if _, aborted := in.(TurnAborted); aborted {
			if _, back := next.(IntegrityIdle); back {
				return
			}
		}
		if turnOfIntegrity(next) != turnOfIntegrity(prev) {
			t.Fatalf("turn changed from %q to %q", turnOfIntegrity(prev), turnOfIntegrity(next))
		}
	})
}
