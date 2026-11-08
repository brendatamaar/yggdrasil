package verify

import (
	"math/rand"
	"testing"
)

// An attempt never decreases across any input sequence of one turn, except
// that a Retry out of Skipped (nothing ran, no attempt kept) starts at 1.
func TestPropAttemptNeverDecreases(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		g := &gen{r: r, now: epoch, turn: 1}
		v := newAggregate()
		best := map[int]Attempt{}
		for i := 0; i < propSteps; i++ {
			v, _ = Step(v, g.next(v))
			seq := seqOf(v.State)
			if _, skipped := v.State.(Skipped); skipped {
				delete(best, seq)
				continue
			}
			a := attemptOf(v.State)
			if a == 0 {
				continue
			}
			if a < best[seq] {
				t.Fatalf("step %d: attempt %d after %d in turn %d (%T)", i, a, best[seq], seq, v.State)
			}
			best[seq] = a
		}
	})
}

func TestRetryContinuesCountThroughCancelAndUnverifiable(t *testing.T) {
	retry := Retry{Facts: okFacts, Plan: planPlan, BootBuild: "boot2", VerifyBuild: "ver2", Now: epoch}
	live := agg(Verifying{Turn: "t1", TurnSeq: 1, Attempt: 2, Build: "ver1", Plan: planPlan})
	cancelled, _ := Step(live, Cancel{Now: epoch})
	if attemptOf(cancelled.State) != 2 {
		t.Fatalf("Cancelled attempt = %d, want 2", attemptOf(cancelled.State))
	}
	again, _ := Step(cancelled, retry)
	if attemptOf(again.State) != 3 {
		t.Fatalf("Retry after Cancel: attempt = %d, want 3", attemptOf(again.State))
	}
	unq, _ := Step(live, EnqueueFailed{Build: "ver1", Now: epoch})
	if u, ok := unq.State.(Unverifiable); !ok || u.Attempt != 2 {
		t.Fatalf("EnqueueFailed state = %#v", unq.State)
	}
	if again, _ := Step(unq, retry); attemptOf(again.State) != 3 {
		t.Fatalf("Retry after could_not_queue: attempt = %d, want 3", attemptOf(again.State))
	}
	// Skipped keeps no attempt: Retry from it starts at 1.
	if again, _ := Step(agg(Skipped{Turn: "t1", TurnSeq: 1, Reason: SkipDisabled}), retry); attemptOf(again.State) != 1 {
		t.Fatalf("Retry from Skipped: attempt = %d, want 1", attemptOf(again.State))
	}
}

func TestTurnOrderIsBySeqNotLabel(t *testing.T) {
	// "a" sorts before "z", but seq decides.
	v := agg(Passed{Turn: "a", TurnSeq: 5, Attempt: 1, Build: "b"})
	in := TurnEnded{Turn: "z", Seq: 4, Facts: okFacts, Plan: planPlan, VerifyBuild: "v", Now: epoch}
	if got, cmds := Step(v, in); changed(got, v) || len(cmds) != 0 {
		t.Error("an older seq with a larger label was accepted")
	}
	in.Turn, in.Seq = "0", 6
	if got, _ := Step(v, in); seqOf(got.State) != 6 {
		t.Error("a newer seq with a smaller label was rejected")
	}
	in.Seq = 0
	if got, cmds := Step(agg(None{}), in); got.State != (None{}) || len(cmds) != 0 {
		t.Error("seq 0 was accepted")
	}
}
