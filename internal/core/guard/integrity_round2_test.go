package guard

import (
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func hasUnverifiableAudit(cmds []Command, turn TurnID) bool {
	for _, c := range cmds {
		if a, ok := c.(Audit); ok && a.Entry.Outcome == "unverifiable" && a.Entry.Turn == turn {
			return true
		}
	}
	return false
}

func hasFlag(cmds []Command, turn TurnID) bool {
	for _, c := range cmds {
		if f, ok := c.(FlagTask); ok && f.Turn == turn {
			return true
		}
	}
	return false
}

// S2: a TurnStarts for a different turn resolves a live check as Unverifiable
// and starts the new turn, so a later Ask turn is never unchecked.
func TestIntegritySupersedeLiveCheck(t *testing.T) {
	for name, live := range map[string]Integrity{
		"arming":   Arming{Turn: "t1"},
		"armed":    Armed{Turn: "t1", Before: snapA},
		"checking": Checking{Turn: "t1", Before: snapA},
	} {
		t.Run(name+"/ask", func(t *testing.T) {
			got, cmds := StepIntegrity(live, TurnStarts{Turn: "t2", Mode: core.ModeAsk})
			if !reflect.DeepEqual(got, Arming{Turn: "t2"}) {
				t.Fatalf("state %#v", got)
			}
			if !hasUnverifiableAudit(cmds, "t1") || !hasFlag(cmds, "t1") {
				t.Fatalf("old turn not resolved: %#v", cmds)
			}
			if hasStop(cmds) {
				t.Fatalf("superseded turn is already stopped: %#v", cmds)
			}
			if last := cmds[len(cmds)-1]; !reflect.DeepEqual(last, TakeSnapshot{Turn: "t2", Phase: PhaseBefore}) {
				t.Fatalf("new Before snapshot missing: %#v", cmds)
			}
		})
		t.Run(name+"/code", func(t *testing.T) {
			got, cmds := StepIntegrity(live, TurnStarts{Turn: "t2", Mode: core.ModeCode})
			if !reflect.DeepEqual(got, IntegrityIdle{}) || !hasUnverifiableAudit(cmds, "t1") || !hasFlag(cmds, "t1") {
				t.Fatalf("got %#v %#v", got, cmds)
			}
		})
		t.Run(name+"/same turn", func(t *testing.T) {
			got, cmds := StepIntegrity(live, TurnStarts{Turn: "t1", Mode: core.ModeAsk})
			if !reflect.DeepEqual(got, live) || len(cmds) != 0 {
				t.Fatalf("got %#v %#v", got, cmds)
			}
		})
	}
}

// Every live check that is replaced leaves an Unverifiable verdict behind.
func TestPropIntegrityEveryAskTurnGetsAVerdict(t *testing.T) {
	driveIntegrity(t, func(prev Integrity, in IntegrityInput, next Integrity, cmds []Command) {
		switch prev.(type) {
		case Arming, Armed, Checking:
		default:
			return
		}
		old := turnOfIntegrity(prev)
		if turnOfIntegrity(next) == old {
			return
		}
		if _, idle := next.(IntegrityIdle); idle {
			if _, ab := in.(TurnAborted); ab && isArming(prev) {
				return // the run never started
			}
		}
		if !hasUnverifiableAudit(cmds, old) || !hasFlag(cmds, old) {
			t.Fatalf("%#v -> %#v by %#v left no verdict: %#v", prev, next, in, cmds)
		}
	})
}

func isArming(s Integrity) bool { _, ok := s.(Arming); return ok }

// The run ended before the Before snapshot returned: nothing can be verified.
func TestIntegrityTurnEndsWhileArming(t *testing.T) {
	got, cmds := StepIntegrity(Arming{Turn: "t1"}, TurnEnds{Turn: "t1"})
	if _, ok := got.(Unverifiable); !ok || !hasStop(cmds) || !hasUnverifiableAudit(cmds, "t1") {
		t.Fatalf("got %#v %#v", got, cmds)
	}
	if got, cmds := StepIntegrity(Arming{Turn: "t1"}, TurnEnds{Turn: "t2"}); !reflect.DeepEqual(got, Arming{Turn: "t1"}) || len(cmds) != 0 {
		t.Fatalf("other turn: %#v %#v", got, cmds)
	}
}

// An abort in Armed or Checking tells the user "could not verify".
func TestIntegrityAbortFlagsTask(t *testing.T) {
	for _, live := range []Integrity{Armed{Turn: "t1", Before: snapA}, Checking{Turn: "t1", Before: snapA}} {
		got, cmds := StepIntegrity(live, TurnAborted{Turn: "t1"})
		if _, ok := got.(Unverifiable); !ok || !hasFlag(cmds, "t1") || !hasUnverifiableAudit(cmds, "t1") || hasStop(cmds) {
			t.Fatalf("%#v: got %#v %#v", live, got, cmds)
		}
	}
}
