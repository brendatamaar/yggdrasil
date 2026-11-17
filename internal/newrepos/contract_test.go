package newrepos

import (
	"fmt"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

// The proposals executor calls the lifecycle through the port
// proposals.NewRepoLifecycle. internal/proposals cannot import this package, so
// the shared contract lives in proposalstest (which imports only core) and runs
// here against the real *Service as well as against the fake. proposals tests
// run it against the fake. NewRepoLifecycle is asserted below, from this
// package, because the interface is declared where it is used.
var _ proposalstest.Lifecycle = (*Service)(nil)

func phaseOf(st corenewrepos.State) proposalstest.OpPhase {
	switch st.(type) {
	case corenewrepos.Reserved, corenewrepos.AwaitingConfirm:
		return proposalstest.PhaseIdle
	case corenewrepos.Initializing, corenewrepos.Publishing:
		return proposalstest.PhaseRunning
	case corenewrepos.Uncertain:
		return proposalstest.PhaseUncertain
	case corenewrepos.Stopped:
		return proposalstest.PhaseStopped
	case corenewrepos.Ready:
		return proposalstest.PhaseReady
	case corenewrepos.Published:
		return proposalstest.PhasePublished
	}
	return ""
}

func TestNewRepoLifecycleContractRealService(t *testing.T) {
	proposalstest.NewRepoLifecycleContract(t, func(t *testing.T) proposalstest.NewRepoRig {
		w := newWorld(t)
		var ops []core.OperationID
		n := 0
		fresh := func() string { n++; return fmt.Sprintf("shop-c%d", n) }
		// Effects counts the starts (distinct attempts that recorded a stage
		// intent) beyond those of the set-up below.
		attempts := func() int {
			seen := map[string]bool{}
			for _, op := range ops {
				effects, err := w.store.ListEffectsByOperation(w.ctx, op)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range effects {
					if e.Stage != "" {
						_, pub := mustStage(e.Stage).(corenewrepos.PublishStage)
						seen[fmt.Sprintf("%s#%d#%t", op, e.Attempt, pub)] = true
					}
				}
			}
			return len(seen)
		}

		initRec := w.reserveAndPropose(fresh())
		readyRec := w.reserveAndPropose(fresh())
		ops = append(ops, initRec.Operation.ID, readyRec.Operation.ID)
		if err := w.svc.ConfirmInit(w.ctx, readyRec.Operation.ID, readyRec.SpecHash, 1); err != nil {
			t.Fatal(err)
		}
		ready := w.waitFor(readyRec.Operation.ID, "ready", isState[corenewrepos.Ready]).(corenewrepos.Ready)
		w.fake.SetLocalHead(w.dir(readyRec.Leaf), "main", ready.InitialHead)
		pv, err := w.svc.PreparePublish(w.ctx, readyRec.Operation.ID,
			PublishRequest{HostInstance: "github.com", Owner: "acme", OwnerKind: corenewrepos.OwnerOrg, Name: readyRec.Leaf}, w.proposal(readyRec))
		if err != nil || len(pv.Problems) != 0 {
			t.Fatalf("prepare publish: %v %v", err, pv.Problems)
		}
		baseline := attempts()

		finish := func(op core.OperationID) {
			w.waitFor(op, "finished", func(st corenewrepos.State) bool {
				return isState[corenewrepos.Ready](st) || isState[corenewrepos.Published](st)
			})
		}
		return proposalstest.NewRepoRig{
			L: w.svc, Init: initRec.Operation.ID, InitHash: initRec.SpecHash,
			Ready: readyRec.Operation.ID, PublishHash: pv.Hash,
			Phase:   func(op core.OperationID) proposalstest.OpPhase { return phaseOf(w.get(op)) },
			Effects: func() int { return attempts() - baseline },
			Finish:  finish,
			Stopped: func(t *testing.T) (core.OperationID, string) {
				// The README stage fails definitely (the folder "changed").
				w.f.on("CreateFile", w.f.count("CreateFile")+1, faultBefore, platform.ErrChanged)
				rec := w.reserveAndPropose(fresh())
				ops = append(ops, rec.Operation.ID)
				if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
					t.Fatal(err)
				}
				w.waitSettled(rec.Operation.ID)
				if s, ok := w.get(rec.Operation.ID).(corenewrepos.Stopped); !ok || !s.Reason.Retryable() {
					t.Fatalf("state = %+v, want a retryable stop", w.get(rec.Operation.ID))
				}
				return rec.Operation.ID, rec.SpecHash
			},
			StoppedForGood: func(t *testing.T) (core.OperationID, string) {
				rec := w.reserveAndPropose(fresh())
				ops = append(ops, rec.Operation.ID)
				if err := w.svc.Reject(w.ctx, rec.Operation.ID); err != nil {
					t.Fatal(err)
				}
				return rec.Operation.ID, rec.SpecHash
			},
		}
	})
}

// The reasons the proposal executor and the lifecycle each decide retry on must
// agree: the executor must not plan a Retry the lifecycle ignores, and must not
// hide one it would take.
func TestStopRetryRulesAgreeBetweenLifecycleAndProposals(t *testing.T) {
	reasons := []corenewrepos.StopReason{
		corenewrepos.StopRejected, corenewrepos.StopUntracked, corenewrepos.StopPathExists, corenewrepos.StopMissingAuthor,
		corenewrepos.StopStageFailed, corenewrepos.StopHostRefused, corenewrepos.StopNameCollision, corenewrepos.StopOriginExists,
		corenewrepos.StopConflict, corenewrepos.StopUnsupported, corenewrepos.StopPreconditions, corenewrepos.StopNotImplemented,
	}
	for _, r := range reasons {
		if err := r.Validate(); err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		if got, want := coreprop.StopRetryable(string(r)), r.Retryable(); got != want {
			t.Errorf("%s: proposals say retryable=%v, the lifecycle says %v", r, got, want)
		}
	}
}
