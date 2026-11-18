package proposalstest

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func newRepoRig(t *testing.T) NewRepoRig {
	f := NewNewRepoFake()
	f.AddInit("op_init", "init-hash")
	f.AddReady("op_ready", "publish-hash")
	return NewRepoRig{
		L: f, Init: "op_init", InitHash: "init-hash", Ready: "op_ready", PublishHash: "publish-hash",
		Phase:   func(op core.OperationID) OpPhase { s, _ := f.State(op); return s.Phase },
		Effects: f.Effects,
		Finish:  f.Finish,
		Stopped: func(t *testing.T) (core.OperationID, string) {
			f.AddInit("op_stopped", "init-hash")
			if err := f.ConfirmInit(context.Background(), "op_stopped", "init-hash", 1); err != nil {
				t.Fatal(err)
			}
			f.Stop("op_stopped", "stage_failed")
			return "op_stopped", "init-hash"
		},
		StoppedForGood: func(t *testing.T) (core.OperationID, string) {
			f.AddInit("op_final", "init-hash")
			if err := f.ConfirmInit(context.Background(), "op_final", "init-hash", 1); err != nil {
				t.Fatal(err)
			}
			f.Stop("op_final", "rejected")
			return "op_final", "init-hash"
		},
	}
}

func TestNewRepoLifecycleContractFake(t *testing.T) { NewRepoLifecycleContract(t, newRepoRig) }

// The fake's own scripted faults: a lost reply happens after the effect, a
// failure before it leaves nothing, state changes are reported in order.
func TestNewRepoFakeFaultsAndOrder(t *testing.T) {
	ctx := context.Background()
	f := NewNewRepoFake()
	f.AddInit("op_a", "h")
	var seen []OpPhase
	f.OnChange(func(c Change) { seen = append(seen, c.State.Phase) })
	boom := errors.New("lost")
	f.Script(FailBefore, boom)
	if err := f.ConfirmInit(ctx, "op_a", "h", 1); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if f.Effects() != 0 || len(seen) != 0 {
		t.Fatal("a failure before the call had an effect")
	}
	f.Script(FailAfter, boom)
	if err := f.ConfirmInit(ctx, "op_a", "h", 1); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if s, _ := f.State("op_a"); f.Effects() != 1 || s.Phase != PhaseRunning {
		t.Fatal("a lost reply must leave the work started")
	}
	f.GoUncertain("op_a")
	f.ScriptReconcile(PhaseReady, "")
	if err := f.ReconcileOperation(ctx, "op_a"); err != nil {
		t.Fatal(err)
	}
	want := []OpPhase{PhaseRunning, PhaseUncertain, PhaseReady}
	if len(seen) != len(want) {
		t.Fatalf("changes = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("changes = %v, want %v", seen, want)
		}
	}
	if f.Confirms() != 2 || f.Reconciles() != 1 {
		t.Fatalf("confirms %d, reconciles %d", f.Confirms(), f.Reconciles())
	}
}
