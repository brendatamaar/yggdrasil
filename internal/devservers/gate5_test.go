package devservers_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

// failNth fails the n-th Commit call (1-based) once.
type failNth struct {
	devservers.Store
	n     int32
	calls atomic.Int32
}

func (f *failNth) Commit(ctx context.Context, fn func(devservers.Tx) error) error {
	if f.calls.Add(1) == f.n {
		return errors.New("disk is busy")
	}
	return f.Store.Commit(ctx, fn)
}

// A failed commit of Started must not strand the server in Starting with no
// pid: the input is retried.
func TestStartedIsRetriedWhenTheCommitFails(t *testing.T) {
	r := newRig(t)
	r.build(&failNth{Store: r.store, n: 2}) // 1 = the start, 2 = Started
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pid recorded after the failed commit", func() bool { return r.get().Pid != 0 })
}

// failRange fails Commit calls from..to (1-based).
type failRange struct {
	devservers.Store
	from, to int32
	calls    atomic.Int32
}

func (f *failRange) Commit(ctx context.Context, fn func(devservers.Tx) error) error {
	if n := f.calls.Add(1); n >= f.from && n <= f.to {
		return errors.New("disk is busy")
	}
	return f.Store.Commit(ctx, fn)
}

// When Started is never recorded (every try fails), the exit that follows must
// still end the attempt: the server is not left in Starting.
func TestExitAfterAnUnrecordedStartedEndsTheAttempt(t *testing.T) {
	r := newRig(t)
	fr := &failRange{Store: r.store, from: 2, to: 6} // the start is call 1; the 5 tries of Started fail
	r.build(fr)
	if _, err := r.svc.Start(context.Background(), r.target, "dev"); err != nil {
		t.Fatal(err)
	}
	v := r.waitStatus("starting")
	eventually(t, "the Started tries to be used up", func() bool { return fr.calls.Load() >= 6 })
	_ = r.fake.KillTask(devservers.ProcessID(v.ID, v.Attempt))
	r.waitStatus("failed")
}
