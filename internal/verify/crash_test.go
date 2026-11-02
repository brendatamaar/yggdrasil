package verify_test

import (
	"context"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// A verification must never stay Verifying or Bootstrapping because its build
// result was malformed or never delivered (gate 1 carry-in).
func TestMalformedBuildResultsNeverStickTheVerification(t *testing.T) {
	finished := func(r *rig, id core.BuildID, spec corebuilds.Spec, result corebuilds.Result, steps []corebuilds.StepState) corebuilds.Finished {
		at := r.runnerTime()
		return corebuilds.Finished{ID: id, Spec: spec, Actor: "system", StartedAt: &at, FinishedAt: at, Result: result, Steps: steps}
	}
	cases := []struct {
		name   string
		result corebuilds.Result
		steps  func(n int) []corebuilds.StepState
	}{
		{"failed step outside the chain", corebuilds.ResultFailed{Step: 99}, doneSteps},
		{"negative failed step", corebuilds.ResultFailed{Step: -1}, doneSteps},
		{"failed but the step succeeded", corebuilds.ResultFailed{Step: 0}, doneSteps},
		{"failed with exit code 0", corebuilds.ResultFailed{Step: 0}, func(n int) []corebuilds.StepState {
			return []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, Outcome: corebuilds.Failed{ExitCode: 0}}}
		}},
		{"failed but no step records", corebuilds.ResultFailed{Step: 0}, func(int) []corebuilds.StepState { return nil }},
		{"failed but the step was interrupted", corebuilds.ResultFailed{Step: 0}, func(int) []corebuilds.StepState {
			return []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, Outcome: corebuilds.Interrupted{}}}
		}},
		{"no result at all", nil, doneSteps},
		{"unknown start failure", corebuilds.ResultFailed{Step: 0}, func(int) []corebuilds.StepState {
			return []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, Outcome: corebuilds.StartFailed{Reason: "gone"}}}
		}},
	}
	for _, c := range cases {
		for _, stage := range []string{"bootstrap", "verify"} {
			t.Run(c.name+"/"+stage, func(t *testing.T) {
				r := newRig(t)
				if stage == "verify" {
					r.tc.bootstrap = nil
				}
				r.runner.Hold = true
				r.reconcile()
				var build core.BuildID
				var spec corebuilds.Spec
				for _, id := range r.runner.Enqueued() {
					build = id
					st, _ := r.runner.Get(context.Background(), id)
					spec = st.(corebuilds.Queued).Spec
				}
				r.runner.Put(build, finished(r, build, spec, c.result, c.steps(len(spec.Steps))))
				r.settle()
				// The run may be resumed once (a new build, attempt 2), but it must
				// never still wait for the build whose result was malformed.
				switch s := r.state().(type) {
				case cv.Bootstrapping:
					if s.Build == build {
						t.Fatalf("stuck on %s: %#v", build, s)
					}
				case cv.Verifying:
					if s.Build == build {
						t.Fatalf("stuck on %s: %#v", build, s)
					}
				}
				if !contains(r.auditActions(), "verify.interrupted") && !contains(r.auditActions(), "verify.failed") {
					t.Fatalf("the malformed result was not recorded: %v", r.auditActions())
				}
			})
		}
	}
}

func doneSteps(n int) []corebuilds.StepState {
	out := make([]corebuilds.StepState, n)
	for i := range out {
		out[i] = corebuilds.StepDone{Attempt: 1, Outcome: corebuilds.Succeeded{}}
	}
	return out
}

// The core drops a result it cannot use; the shell must then mark the run
// interrupted instead of waiting for ever.
func TestWatchdogMarksARefusedResultInterrupted(t *testing.T) {
	r := newRig(t)
	r.tc.bootstrap = nil
	r.runner.Hold = true
	r.reconcile()
	rec := r.records()[0]
	build := rec.State.(cv.Verifying).Build
	// Step 99 is outside the 2-step chain: Step ignores it (V13).
	r.svc.FeedResultForTest(context.Background(), rec.Task, rec.Repo, rec.Worktree, build, cv.BuildFailedAt{Step: 99, ExitCode: 1})
	if i, ok := r.state().(cv.Interrupted); !ok || i.Stage != cv.StageVerify {
		t.Fatalf("state = %#v, want Interrupted", r.state())
	}
}

// A result for a build that is not the current one does nothing and is not
// turned into an interruption.
func TestStaleResultDoesNotInterruptTheCurrentRun(t *testing.T) {
	r := newRig(t)
	r.tc.bootstrap = nil
	r.runner.Hold = true
	r.reconcile()
	rec := r.records()[0]
	r.svc.FeedResultForTest(context.Background(), rec.Task, rec.Repo, rec.Worktree, "bld_other", cv.BuildPassed{})
	if _, ok := r.state().(cv.Verifying); !ok {
		t.Fatalf("state = %#v", r.state())
	}
}

// Crash between the commit and the enqueue: the next pass queues the same ID,
// and only one build exists.
func TestCrashBeforeEnqueueRequeuesTheSameBuild(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	r.reconcile()
	rec := r.records()[0]
	build := rec.State.(cv.Bootstrapping).Build
	r.runner.Forget(build) // the process died before EnqueueWithID took effect

	r.svc = r.newService() // restart
	r.reconcile()
	if r.runner.Calls(build) != 2 {
		t.Fatalf("enqueue calls for %s = %d, want 2", build, r.runner.Calls(build))
	}
	if got := r.runner.Enqueued(); len(got) != 1 || got[0] != build {
		t.Fatalf("builds = %v, want only %s", got, build)
	}
	// And a build that exists is not queued again.
	r.reconcile()
	if r.runner.Calls(build) != 2 {
		t.Fatal("an existing build was queued again")
	}
	r.runner.Hold = false
	r.runner.Finish(build)
	r.settle()
	if _, ok := r.state().(cv.Passed); !ok {
		t.Fatalf("state = %#v", r.state())
	}
}

// Restart mid-verify: the run is marked interrupted, then verified again once,
// with no duplicate; the second interruption waits for the user.
func TestRestartMidVerifyResumesOnceWithoutDuplicate(t *testing.T) {
	r := newRig(t)
	r.tc.bootstrap = nil
	r.runner.Hold = true
	r.reconcile()
	first := r.records()[0].State.(cv.Verifying).Build

	// ygg stops: builds marks the running build interrupted.
	r.runner.FinishWith(first, "interrupt")
	r.svc = r.newService()
	r.reconcile()
	v, ok := r.state().(cv.Verifying)
	if !ok || v.Attempt != 2 || v.Build == first {
		t.Fatalf("state = %#v, want a new attempt 2", r.state())
	}
	if len(r.runner.Enqueued()) != 2 {
		t.Fatalf("builds = %v", r.runner.Enqueued())
	}
	// More passes, and another restart, add nothing.
	r.reconcile()
	r.svc = r.newService()
	r.reconcile()
	if len(r.runner.Enqueued()) != 2 {
		t.Fatalf("duplicate build: %v", r.runner.Enqueued())
	}
	// Interrupted again: no second automatic resume.
	r.runner.FinishWith(v.Build, "interrupt")
	r.svc = r.newService()
	r.settle()
	if i, ok := r.state().(cv.Interrupted); !ok || i.Attempt != 2 {
		t.Fatalf("state = %#v, want Interrupted attempt 2", r.state())
	}
	if len(r.runner.Enqueued()) != 2 {
		t.Fatalf("builds = %v", r.runner.Enqueued())
	}
}

func TestInterruptedIsNotResumedWhenTheTaskMovedOn(t *testing.T) {
	r := newRig(t)
	r.tc.bootstrap = nil
	r.runner.Hold = true
	r.reconcile()
	first := r.records()[0].State.(cv.Verifying).Build
	r.runner.FinishWith(first, "interrupt")
	r.store.SetReview("tsk_1", "", 0) // the task runs another turn
	r.settle()
	if _, ok := r.state().(cv.Interrupted); !ok {
		t.Fatalf("state = %#v", r.state())
	}
}

// Duplicate delivery of one build result changes the state once.
func TestDuplicateDeliveryIsHarmless(t *testing.T) {
	r := newRig(t)
	r.tc.bootstrap = nil
	r.runner.Hold = true
	r.reconcile()
	rec := r.records()[0]
	build := rec.State.(cv.Verifying).Build
	r.runner.FinishWith(build, "pass")
	for i := 0; i < 3; i++ {
		r.svc.FeedResultForTest(context.Background(), rec.Task, rec.Repo, rec.Worktree, build, cv.BuildPassed{})
		r.reconcile()
	}
	if _, ok := r.state().(cv.Passed); !ok {
		t.Fatalf("state = %#v", r.state())
	}
	passed := 0
	for _, a := range r.auditActions() {
		if a == "verify.passed" {
			passed++
		}
	}
	if passed != 1 {
		t.Fatalf("%d passed audit entries, want 1", passed)
	}
}

// Concurrent turn ends, from one service and from two (a restart overlapping
// the old process), start exactly one run.
func TestConcurrentTurnEndsStartOneRun(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	other := r.newService()
	var wg sync.WaitGroup
	for _, svc := range []interface{ Reconcile(context.Context) error }{r.svc, r.svc, other, other, r.svc} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.Reconcile(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, ok := r.state().(cv.Bootstrapping); !ok {
		t.Fatalf("state = %#v", r.state())
	}
	if n := len(r.runner.Enqueued()); n != 1 {
		t.Fatalf("%d builds started, want 1", n)
	}
	starts := 0
	for _, a := range r.auditActions() {
		if a == "verify.bootstrapping" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("%d start audit entries, want 1", starts)
	}
}

// A queued build that the builds service reports as finished long before the
// loop looks (the subscription missed it) is picked up by the poll.
func TestPollPicksUpAResultTheWakeMissed(t *testing.T) {
	r := newRig(t)
	r.tc.bootstrap = []builds.Step(nil)
	r.runner.Hold = true
	r.reconcile()
	build := r.records()[0].State.(cv.Verifying).Build
	r.runner.FinishWith(build, "fail:1:5")
	r.reconcile()
	f, ok := r.state().(cv.Failed)
	if !ok || f.Step != 1 || f.Reason != (cv.ExitCode{Code: 5}) {
		t.Fatalf("state = %#v", r.state())
	}
}
