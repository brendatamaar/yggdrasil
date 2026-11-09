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
