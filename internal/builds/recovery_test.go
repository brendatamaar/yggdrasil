package builds_test

import (
	"context"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// A finished build forgets its start records even when no status service is
// wired; S1 still holds for a replayed StartStep while it runs.
func TestStartedPrunedWithoutStatusService(t *testing.T) {
	h := newHarness(t, func(d *builds.Deps) { d.Status = nil })
	h.start()
	id := h.enqueue(steps(block))
	h.wait(id, isRunning)
	for i := 0; i < 5000 && len(h.fake.Started()) == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	cmd := corebuilds.StartStep{BuildID: id, Index: 0, Attempt: 1, RepoID: "rep_a", Target: corebuilds.MainCheckout{}, Argv: block}
	h.svc.StartStepAgain(context.Background(), cmd)
	h.svc.StartStepAgain(context.Background(), cmd)
	if n := len(h.fake.Started()); n != 1 {
		t.Fatalf("S1: started %d times", n)
	}
	if err := h.svc.Cancel(context.Background(), id, "user"); err != nil {
		t.Fatal(err)
	}
	h.finished(id)
	if n := h.svc.StartedRecords(); n != 0 {
		t.Fatalf("%d start records left after the build finished", n)
	}
}

// A panic while performing the commands of a committed step must not leave
// the build Running without a process: the core is told the process is lost
// and the queue moves on.
func TestInterpretPanicRecovers(t *testing.T) {
	h := newHarness(t, func(d *builds.Deps) {
		inner := d.Start
		d.Start = func(ctx context.Context, spec platform.Spec) (platform.Process, error) {
			if len(spec.Argv) > 1 && spec.Argv[1] == "boom" {
				panic("start exploded")
			}
			return inner(ctx, spec)
		}
	})
	h.start()
	bad := h.enqueue(steps([]string{"fake", "boom"}))
	next := h.enqueue(steps(exit(0)))
	f := h.finished(bad)
	if _, ok := f.Result.(corebuilds.ResultInterrupted); !ok {
		t.Fatalf("result %#v, want interrupted", f.Result)
	}
	n := h.finished(next)
	if _, ok := n.Result.(corebuilds.ResultSucceeded); !ok {
		t.Fatalf("queued build did not run after the panic: %#v", n.Result)
	}
}
