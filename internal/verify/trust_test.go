package verify_test

import (
	"testing"

	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// Trust revoked while the bootstrap stage runs: the verify stage is never
// queued (SF-09, invariant 10).
func TestTrustRevokedBetweenStagesQueuesNoSecondStage(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	r.reconcile()
	boot := r.records()[0].State.(cv.Bootstrapping)
	r.store.SetTrusted("rep_1", false)
	r.runner.FinishWith(boot.Build, "pass")
	r.settle()
	if got := r.runner.Enqueued(); len(got) != 1 || got[0] != boot.Build {
		t.Fatalf("builds = %v, want only the bootstrap build", got)
	}
	if c, ok := r.state().(cv.Cancelled); !ok || c.Cause != cv.CancelNotTrusted {
		t.Fatalf("state = %#v, want the chain stopped with cause not_trusted", r.state())
	}
}

// The same check on the path that queues a build again (a crash after the
// commit).
func TestTrustRevokedBeforeRequeueQueuesNothing(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	r.reconcile()
	boot := r.records()[0].State.(cv.Bootstrapping)
	r.runner.Forget(boot.Build)
	r.store.SetTrusted("rep_1", false)
	r.settle()
	if got := r.runner.Enqueued(); len(got) != 0 {
		t.Fatalf("builds = %v, want none", got)
	}
	if _, ok := r.state().(cv.Cancelled); !ok {
		t.Fatalf("state = %#v", r.state())
	}
}
