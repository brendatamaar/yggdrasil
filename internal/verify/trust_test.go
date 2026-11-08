package verify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
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

// The admission predicate: the live view none or trusted passes, untrusted
// does not (SF-09). A repo with no agent config has the view none.
func TestTrustPredicateIsTheAdmissionView(t *testing.T) {
	cases := []struct {
		view guard.TrustView
		ok   bool
	}{
		{guard.TrustNone, true}, {guard.TrustTrusted, true}, {guard.TrustUntrusted, false}, {"", false}, {"weird", false},
	}
	for _, c := range cases {
		t.Run(string(c.view), func(t *testing.T) {
			r := newRig(t)
			r.store.SetTrustView("rep_1", c.view)
			r.settle()
			if got := len(r.runner.Enqueued()); (got > 0) != c.ok {
				t.Fatalf("view %q: %d builds started, want started=%v", c.view, got, c.ok)
			}
			if !c.ok {
				if u, ok := r.state().(cv.Unverifiable); !ok || u.Reason != cv.UnverifiableNotTrusted {
					t.Fatalf("state = %#v, want Unverifiable not trusted", r.state())
				}
			}
		})
	}
}

// Revoked mid-chain for a repo whose view turns untrusted (config appeared).
func TestTrustViewTurningUntrustedMidChainCancels(t *testing.T) {
	r := newRig(t)
	r.store.SetTrustView("rep_1", guard.TrustNone)
	r.runner.Hold = true
	r.reconcile()
	boot := r.records()[0].State.(cv.Bootstrapping)
	r.store.SetTrustView("rep_1", guard.TrustUntrusted)
	r.runner.FinishWith(boot.Build, "pass")
	r.settle()
	if c, ok := r.state().(cv.Cancelled); !ok || c.Cause != cv.CancelNotTrusted {
		t.Fatalf("state = %#v, want Cancelled not_trusted", r.state())
	}
}

// A trust read error is not a "no": nothing is queued, the run is not
// cancelled, and the next pass goes on.
func TestTrustReadErrorQueuesNothingAndIsRetried(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	r.reconcile()
	boot := r.records()[0].State.(cv.Bootstrapping)
	r.runner.Forget(boot.Build)
	r.store.TrustErr = func() error { return errors.New("scan failed") }
	r.settle()
	if got := r.runner.Enqueued(); len(got) != 0 {
		t.Fatalf("builds = %v, want none while the trust read fails", got)
	}
	if _, ok := r.state().(cv.Bootstrapping); !ok {
		t.Fatalf("state = %#v, want still Bootstrapping", r.state())
	}
	r.store.TrustErr = nil
	r.settle()
	if got := r.runner.Enqueued(); len(got) != 1 {
		t.Fatalf("builds = %v, want the build queued on a later pass", got)
	}
}

// A trust read error while entering the chain starts nothing, then recovers.
func TestTrustReadErrorOnEnterStartsNothing(t *testing.T) {
	r := newRig(t)
	r.store.TrustErr = func() error { return errors.New("scan failed") }
	_ = r.svc.Reconcile(context.Background())
	if got := r.runner.Enqueued(); len(got) != 0 {
		t.Fatalf("builds = %v, want none", got)
	}
	for _, s := range r.states() {
		if u, ok := s.(cv.Unverifiable); ok && u.Reason == cv.UnverifiableNotTrusted {
			t.Fatalf("a read error must not be recorded as not trusted: %#v", u)
		}
	}
	r.store.TrustErr = nil
	r.settle()
	if len(r.runner.Enqueued()) == 0 {
		t.Fatal("verification did not start after the trust read recovered")
	}
}
