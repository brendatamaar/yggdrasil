package builds_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// activeStore wraps a store so ListActive can fail, or fail with a partial result.
type activeStore struct {
	builds.BuildStore
	failures atomic.Int32 // ListActive fails this many times, then works
	partial  bool         // a failure still returns the parseable rows
	calls    atomic.Int32
}

func (a *activeStore) ListActive(ctx context.Context) ([]corebuilds.State, error) {
	a.calls.Add(1)
	list, err := a.BuildStore.ListActive(ctx)
	if a.failures.Add(-1) >= 0 {
		if a.partial {
			return list, errors.New("build bld_corrupt: unreadable row")
		}
		return nil, errors.New("database is busy")
	}
	return list, err
}

func seedRunning(t *testing.T, h *harness, id string) {
	t.Helper()
	t0 := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)
	run := corebuilds.Running{ID: core.BuildID(id), Spec: steps(exit(0)), QueuedAt: t0, StartedAt: t0, Actor: "user",
		Steps: []corebuilds.StepState{corebuilds.StepRunning{Attempt: 1, StartedAt: t0}}}
	err := h.store.Commit(context.Background(), func(tx builds.BuildTx) error {
		_, err := tx.SaveBuild(context.Background(), run, builds.NewBuild)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReconcileRetriesAFailingListActive(t *testing.T) {
	var wrap *activeStore
	h := newHarness(t, func(d *builds.Deps) {
		wrap = &activeStore{BuildStore: d.Store}
		wrap.failures.Store(2)
		d.Store = wrap
		d.ReconcileBackoff = time.Millisecond
	})
	seedRunning(t, h, "bld_old")
	h.start()
	if _, ok := h.finished("bld_old").Result.(corebuilds.ResultInterrupted); !ok {
		t.Error("the running build should be marked interrupted after the retries")
	}
	if n := wrap.calls.Load(); n != 3 {
		t.Errorf("ListActive calls = %d, want 3", n)
	}
}

func TestReconcileProcessesParseableRowsWhenOneIsCorrupt(t *testing.T) {
	h := newHarness(t, func(d *builds.Deps) {
		w := &activeStore{BuildStore: d.Store, partial: true}
		w.failures.Store(1)
		d.Store = w
		d.ReconcileBackoff = time.Millisecond
	})
	seedRunning(t, h, "bld_good")
	h.start()
	if _, ok := h.finished("bld_good").Result.(corebuilds.ResultInterrupted); !ok {
		t.Error("the parseable running build should still be marked interrupted")
	}
}
