package devservers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// The no-port-block warning follows the state: gone at rest, back on a start.
func TestNoBlockWarningIsDerivedFromState(t *testing.T) {
	r := newRig(t)
	r.blocks.Err = worktreeenv.ErrRangeFull
	r.toRunning(20001)
	if r.get().Warning == "" {
		t.Fatal("no warning while running without a block")
	}
	if _, err := r.svc.Stop(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	if v := r.waitStatus("stopped"); v.Warning != "" {
		t.Errorf("warning at rest: %q", v.Warning)
	}
	// A main checkout never has a block and never warns.
	r.target = devservers.Target{Repo: "rep_1"}
	r.toRunning(3000)
	if v := r.get(); v.Warning != "" {
		t.Errorf("main checkout warns: %q", v.Warning)
	}
}

func TestFailedMessageIsClearedOnTheNextStart(t *testing.T) {
	r := newRig(t)
	r.fake.Push(platformtest.Script{Exit: 3})
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	if v := r.waitStatus("failed"); v.Message == "" {
		t.Fatal("no failure message")
	}
	if v, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil || v.Message != "" {
		t.Errorf("start after a failure: message %q, %v", v.Message, err)
	}
}

// A lost wake would leave a server in Starting or Stopping for good: wakes are
// retried like exits.
func TestWakeIsRetriedWhenTheCommitFails(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pid", func() bool { return r.get().Pid != 0 })
	r.store.FailCommit = errors.New("disk is busy")
	r.clock.Advance(dsc.SettleAfter) // the settle wake fires and its commit fails once
	r.waitStatus("running")
}

// Stop on a Stopping server escalates, and the grace wake then ends it.
func TestStopWhileStoppingEscalatesAndEndsInFailure(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	k := dsc.Key{Repo: "rep_1", Worktree: "wt_1", CommandKey: "dev"}
	id := devservers.IDOf(k)
	t0 := r.clock.Now()
	if err := r.store.Commit(ctx, func(tx devservers.Tx) error {
		_, err := tx.Save(ctx, id, dsc.RowOf(k, dsc.Stopping{Attempt: 1, Cause: dsc.CauseUserStop, KillAttempt: dsc.KillFirst, Since: t0, Pid: 5}), db.NewDevServer, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Stop(ctx, r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	row, _ := r.store.Get(ctx, id)
	if row.Row.Status != "stopping" || row.Row.KillAttempt != dsc.KillSecond {
		t.Fatalf("row = %+v", row.Row)
	}
	r.clock.Advance(dsc.KillGraceUser)
	if v := r.waitStatus("failed"); v.Fail != dsc.FailStopFailed {
		t.Errorf("view = %+v", v)
	}
	if _, err := r.svc.Start(ctx, r.target, "dev"); err != nil {
		t.Errorf("start after a failed stop: %v", err)
	}
}

func TestByIDAndPlaceholder(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	key := r.target.Key("dev")
	p := r.svc.Placeholder(key)
	if p.ID != devservers.IDOf(key) || p.Status != "stopped" || p.Key != key {
		t.Fatalf("placeholder = %+v", p)
	}
	_, err := r.svc.ByID(ctx, p.ID)
	if c, _ := apperr.ClassOf(err); c != apperr.NotFound {
		t.Fatalf("ByID of a never started server = %v", err)
	}
	r.toRunning(20001)
	v, err := r.svc.ByID(ctx, p.ID)
	if err != nil || v.Key != key || v.Status != "running" {
		t.Errorf("ByID = %+v, %v", v, err)
	}
}
