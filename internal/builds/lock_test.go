package builds_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
)

func lockHarness(t *testing.T) (*harness, *mainlock.Registry) {
	reg := &mainlock.Registry{}
	h := newHarness(t, func(d *builds.Deps) { d.MainLock = reg; d.LockRetry = 10 * time.Millisecond })
	h.start()
	return h, reg
}

func holderOf(reg *mainlock.Registry, r core.RepoID) (mainlock.Owner, bool) {
	h, ok := reg.Holder(r)
	return h.Owner, ok
}

func waitReleased(t *testing.T, reg *mainlock.Registry, repos ...core.RepoID) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for _, r := range repos {
		for {
			if _, ok := reg.Holder(r); !ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("lock on %s never released", r)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestMainBuildHoldsEveryRepoLockUntilTerminal(t *testing.T) {
	h, reg := lockHarness(t)
	id := h.enqueue(steps([]string{"fake", "block"}, exit(0))) // rep_a and rep_b
	h.wait(id, isRunning)
	want := mainlock.Owner{Kind: mainlock.KindBuild, ID: string(id)}
	for _, r := range []core.RepoID{"rep_a", "rep_b"} {
		if o, ok := holderOf(reg, r); !ok || o != want {
			t.Fatalf("holder of %s = %v %v, want %v", r, o, ok, want)
		}
	}
	if err := h.svc.Cancel(context.Background(), id, "user"); err != nil {
		t.Fatal(err)
	}
	h.finished(id)
	waitReleased(t, reg, "rep_a", "rep_b")
}

// A panic in the handling of an input of a running main-checkout build must
// not keep its repo locked: a task or a pull can take the checkout afterwards.
func TestPanicBeforeFinishedReleasesTheMainLock(t *testing.T) {
	reg := &mainlock.Registry{}
	h := newHarness(t, func(d *builds.Deps) { d.MainLock = reg; d.LockRetry = 10 * time.Millisecond })
	var once atomic.Bool
	h.svc.SetPanicHook(func(in corebuilds.Input) {
		if _, ok := in.(corebuilds.StepExited); ok && once.CompareAndSwap(false, true) {
			panic("scripted panic")
		}
	})
	h.start()
	h.enqueue(steps(exit(0)))
	deadline := time.Now().Add(3 * time.Second)
	for !once.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !once.Load() {
		t.Fatal("the hook never fired")
	}
	waitReleased(t, reg, "rep_a")
	rel, _, ok := reg.TryAcquire([]core.RepoID{"rep_a"}, mainlock.Owner{Kind: mainlock.KindTask, ID: "tsk_1"})
	if !ok {
		t.Fatal("a task could not take the checkout after the panic")
	}
	rel()
}

func TestBuildWaitsWhileATaskEditsTheCheckout(t *testing.T) {
	h, reg := lockHarness(t)
	rel, _, ok := reg.TryAcquire([]core.RepoID{"rep_b"}, mainlock.Owner{Kind: mainlock.KindTask, ID: "tsk_1"})
	if !ok {
		t.Fatal("setup")
	}
	id := h.enqueue(steps(exit(0), exit(0)))
	time.Sleep(150 * time.Millisecond)
	if st, _ := h.svc.Get(context.Background(), id); !isQueued(st) {
		t.Fatalf("build should stay queued, got %#v", st)
	}
	if len(h.fake.Started()) != 0 {
		t.Fatal("no step may start while a task holds the checkout")
	}
	if _, held := holderOf(reg, "rep_a"); held {
		t.Fatal("a denied build must not keep a partial lock")
	}
	rel()
	if _, ok := h.finished(id).Result.(corebuilds.ResultSucceeded); !ok {
		t.Fatal("build should run once the task is done")
	}
	waitReleased(t, reg, "rep_a", "rep_b")
}

func TestWorktreeBuildTakesNoLock(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := &mainlock.Registry{}
	h := newHarness(t, func(d *builds.Deps) {
		d.MainLock = reg
		d.Locator = locator{repos: map[core.RepoID]string{"rep_a": "/x"}, worktrees: map[core.WorktreeID]string{"wt_1": wt}}
	})
	h.start()
	_, _, _ = reg.TryAcquire([]core.RepoID{"rep_a"}, mainlock.Owner{Kind: mainlock.KindTask, ID: "tsk_1"})
	spec := steps(exit(0))
	spec.Target = corebuilds.InWorktree{WorktreeID: "wt_1"}
	h.finished(h.enqueue(spec))
}

func TestLockReleasedOnFailureAndNextBuildRuns(t *testing.T) {
	h, reg := lockHarness(t)
	bad := h.enqueue(steps(exit(1)))
	good := h.enqueue(steps(exit(0)))
	if _, ok := h.finished(bad).Result.(corebuilds.ResultSucceeded); ok {
		t.Fatal("expected failure")
	}
	if _, ok := h.finished(good).Result.(corebuilds.ResultSucceeded); !ok {
		t.Fatal("second build should run after the first released the checkout")
	}
	waitReleased(t, reg, "rep_a")
}

func TestLockReleasedWhenRecoveryEndsTheBuild(t *testing.T) {
	h, reg := lockHarness(t)
	id := h.enqueue(steps([]string{"fake", "block"}))
	h.wait(id, isRunning)
	h.svc.Post(id, corebuilds.ProcessLost{Now: time.Now()})
	h.finished(id)
	waitReleased(t, reg, "rep_a")
}
