package verify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

func isQueued(t *testing.T, r *rig, id core.BuildID) bool {
	t.Helper()
	st, err := r.runner.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, queued := st.(corebuilds.Queued)
	return queued
}

// A cancel that never reached the builds service (error, or a crash after the
// commit) is repaired by a later pass.
func TestLostCancelOfAUserCancelIsRepaired(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	r.reconcile()
	build := r.records()[0].State.(cv.Bootstrapping).Build
	r.runner.CancelErr = func(core.BuildID) error { return errors.New("builds busy") }
	if _, err := r.svc.Cancel(context.Background(), r.records()[0].ID); err != nil {
		t.Fatal(err)
	}
	if !isQueued(t, r, build) {
		t.Fatal("the build should still be queued after the failed cancel")
	}
	r.reconcile() // still failing: stays queued
	if !isQueued(t, r, build) {
		t.Fatal("still failing, still queued")
	}
	r.runner.CancelErr = nil
	r.svc = r.newService() // restart
	r.reconcile()
	if isQueued(t, r, build) {
		t.Fatal("the orphaned build was not cancelled")
	}
}

func TestLostCancelOfASupersededBuildIsRepaired(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	r.reconcile()
	old := r.records()[0].State.(cv.Bootstrapping).Build
	r.runner.CancelErr = func(core.BuildID) error { return errors.New("builds busy") }
	r.store.SetReview("tsk_1", "turn_2", 2)
	r.reconcile()
	cur := r.records()[0].State.(cv.Bootstrapping).Build
	if cur == old || !isQueued(t, r, old) {
		t.Fatalf("setup: old %s queued=%v, current %s", old, isQueued(t, r, old), cur)
	}
	r.runner.CancelErr = nil
	r.reconcile()
	if isQueued(t, r, old) {
		t.Fatal("the superseded build was not cancelled")
	}
	if !isQueued(t, r, cur) {
		t.Fatal("the current build must not be cancelled")
	}
}

func TestSweepKeepsBuildsVerificationDidNotStart(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	if _, err := r.runner.EnqueueWithID(context.Background(), "bld_user_1", corebuilds.Spec{
		Target: corebuilds.InWorktree{WorktreeID: "wt_1"},
		Steps:  []corebuilds.StepSpec{{RepoID: "rep_1", Name: "x", Argv: exit(0)}},
	}, "user"); err != nil {
		t.Fatal(err)
	}
	r.reconcile()
	r.reconcile()
	if !isQueued(t, r, "bld_user_1") {
		t.Fatal("a build verification did not start must be left alone")
	}
}
