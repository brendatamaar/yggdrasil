package verify_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

func TestBootstrapThenVerifyPasses(t *testing.T) {
	r := newRig(t)
	r.settle()
	p, ok := r.state().(cv.Passed)
	if !ok {
		t.Fatalf("state = %#v, want Passed", r.state())
	}
	if p.TurnSeq != 1 || p.Attempt != 1 {
		t.Fatalf("passed = %+v", p)
	}
	builds := r.runner.Enqueued()
	if len(builds) != 2 {
		t.Fatalf("%d builds, want bootstrap + verify", len(builds))
	}
	// Every build is queued in the task's worktree, by the system actor.
	for _, id := range builds {
		st, _ := r.runner.Get(context.Background(), id)
		fin := st.(corebuilds.Finished)
		if wt, ok := fin.Spec.Target.(corebuilds.InWorktree); !ok || wt.WorktreeID != "wt_1" {
			t.Errorf("build %s target = %#v", id, fin.Spec.Target)
		}
		if fin.Actor != "system" {
			t.Errorf("build %s actor = %q", id, fin.Actor)
		}
	}
	want := []string{"verify.bootstrapping", "verify.verifying", "verify.passed"}
	if got := r.auditActions(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit = %v, want %v", got, want)
	}
	if len(r.hub.Published()) != 3 {
		t.Fatalf("published %v", r.hub.Published())
	}
	rec := r.records()[0]
	if rec.BootHash == "" {
		t.Fatal("the dependency hash of the bootstrap was not kept")
	}
}

func TestNoBootstrapWhenHashUnchangedAndAgainWhenItChanges(t *testing.T) {
	r := newRig(t)
	r.settle()
	first := len(r.runner.Enqueued())
	// A newer turn with the same dependencies verifies without a bootstrap.
	r.store.SetReview("tsk_1", "turn_2", 2)
	r.settle()
	if got := len(r.runner.Enqueued()) - first; got != 1 {
		t.Fatalf("%d builds for the second turn, want only the verify build", got)
	}
	// The agent edits the lockfile: the next turn bootstraps again (WE-05).
	r.files["deps.lock"] = &fstest.MapFile{Data: []byte("v2")}
	r.store.SetReview("tsk_1", "turn_3", 3)
	r.settle()
	if got := len(r.runner.Enqueued()) - first; got != 3 {
		t.Fatalf("%d builds after the lockfile changed, want verify + bootstrap + verify", got)
	}
	if _, ok := r.state().(cv.Passed); !ok {
		t.Fatalf("state = %#v", r.state())
	}
}

func TestEnterRulesNeedNoBuild(t *testing.T) {
	cases := []struct {
		name  string
		setup func(r *rig)
		want  func(cv.State) bool
	}{
		{"switched off", func(r *rig) { r.store.SetAuto(false) }, func(s cv.State) bool { x, ok := s.(cv.Skipped); return ok && x.Reason == cv.SkipDisabled }},
		{"untrusted repo", func(r *rig) { r.store.SetTrusted("rep_1", false) }, func(s cv.State) bool {
			x, ok := s.(cv.Unverifiable)
			return ok && x.Reason == cv.UnverifiableNotTrusted
		}},
		{"no changes", func(r *rig) { r.probe.Changes[r.dir] = false }, func(s cv.State) bool { x, ok := s.(cv.Skipped); return ok && x.Reason == cv.SkipNoChanges }},
		{"no toolchain", func(r *rig) { delete(r.files, "marker") }, func(s cv.State) bool {
			x, ok := s.(cv.Unverifiable)
			return ok && x.Reason == cv.UnverifiableNoCommand
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.setup(r)
			r.settle()
			if !c.want(r.state()) {
				t.Fatalf("state = %#v", r.state())
			}
			if n := len(r.runner.Enqueued()); n != 0 {
				t.Fatalf("%d builds started", n)
			}
		})
	}
}

func TestGitIsNotReadForDisabledOrUntrusted(t *testing.T) {
	r := newRig(t)
	r.store.SetAuto(false)
	r.settle()
	if r.probe.Calls != 0 {
		t.Fatalf("git was read %d times although verification is off", r.probe.Calls)
	}
}

func TestFailedVerifyKeepsStepAndExitCode(t *testing.T) {
	r := newRig(t)
	r.tc.verify = []builds.Step{step("lint", 0), step("test", 3)}
	r.settle()
	f, ok := r.state().(cv.Failed)
	if !ok {
		t.Fatalf("state = %#v", r.state())
	}
	if f.Stage != cv.StageVerify || f.Step != 1 || f.Reason != (cv.ExitCode{Code: 3}) {
		t.Fatalf("failed = %+v", f)
	}
}

func TestFailedBootstrapNeverRunsVerify(t *testing.T) {
	r := newRig(t)
	r.tc.bootstrap = []builds.Step{step("install", 7)}
	r.settle()
	f, ok := r.state().(cv.Failed)
	if !ok || f.Stage != cv.StageBootstrap {
		t.Fatalf("state = %#v", r.state())
	}
	if n := len(r.runner.Enqueued()); n != 1 {
		t.Fatalf("%d builds, want only the bootstrap", n)
	}
	if r.records()[0].BootHash != "" {
		t.Fatal("a failed bootstrap kept its hash")
	}
}

func TestSendFailureComposesFromRedactedLog(t *testing.T) {
	r := newRig(t)
	r.tc.verify = []builds.Step{step("lint", 0), step("test", 3)}
	r.settle()
	rec := r.records()[0]
	f := rec.State.(cv.Failed)
	r.runner.SetLog(f.Build, 1, "FAIL TestX\nAuthorization: Bearer abcdefghij1234567890\ntoken="+secret+"\n--- done\n")
	if err := r.svc.SendFailure(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	msgs := r.msg.Messages()
	if len(msgs) != 1 || msgs[0].Task != "tsk_1" {
		t.Fatalf("messages = %+v", msgs)
	}
	text := msgs[0].Text
	for _, want := range []string{"test", "code 3", "FAIL TestX"} {
		if !strings.Contains(text, want) {
			t.Errorf("message lacks %q:\n%s", want, text)
		}
	}
	for _, leak := range []string{secret, "abcdefghij1234567890"} {
		if strings.Contains(text, leak) {
			t.Errorf("message leaks %q:\n%s", leak, text)
		}
	}
	if !contains(r.auditActions(), "verify.send_failure") {
		t.Fatalf("audit = %v", r.auditActions())
	}
	// A send does not change the verification: it is not a Step input.
	if _, ok := r.state().(cv.Failed); !ok {
		t.Fatalf("state = %#v", r.state())
	}
}

func sleep() { time.Sleep(5 * time.Millisecond) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestSendFailureRefusals(t *testing.T) {
	r := newRig(t)
	r.settle()
	rec := r.records()[0]
	err := r.svc.SendFailure(context.Background(), rec.ID)
	if c, ok := apperr.ClassOf(err); !ok || c != apperr.Conflict {
		t.Fatalf("passed verification: err = %v", err)
	}
	r2 := newRig(t)
	r2.tc.verify = []builds.Step{step("test", 3)}
	r2.settle()
	r2.msg.Err = errors.New("the task is busy")
	if err := r2.svc.SendFailure(context.Background(), r2.records()[0].ID); err == nil || err.Error() != "the task is busy" {
		t.Fatalf("busy task: err = %v", err)
	}
	if err := r2.svc.SendFailure(context.Background(), "ver_nope"); err == nil {
		t.Fatal("unknown verification accepted")
	} else if c, _ := apperr.ClassOf(err); c != apperr.NotFound {
		t.Fatalf("err = %v", err)
	}
	if contains(r2.auditActions(), "verify.send_failure") {
		t.Fatal("a refused send was audited as sent")
	}
}

func TestRetryStartsANewAttemptButNotOverALiveRun(t *testing.T) {
	r := newRig(t)
	r.tc.verify = []builds.Step{step("test", 3)}
	r.settle()
	r.tc.verify = []builds.Step{step("test", 0)}
	recs, err := r.svc.Retry(context.Background(), "tsk_1")
	if err != nil {
		t.Fatal(err)
	}
	v, ok := recs[0].State.(cv.Verifying)
	if !ok || v.Attempt != 2 {
		t.Fatalf("after retry: %#v", recs[0].State)
	}
	r.settle()
	if p, ok := r.state().(cv.Passed); !ok || p.Attempt != 2 {
		t.Fatalf("state = %#v", r.state())
	}

	// A live run is not replaced.
	h := newRig(t)
	h.runner.Hold = true
	h.reconcile()
	before := len(h.runner.Enqueued())
	if _, err := h.svc.Retry(context.Background(), "tsk_1"); err != nil {
		t.Fatal(err)
	}
	if len(h.runner.Enqueued()) != before {
		t.Fatal("retry replaced a live run")
	}
}

func TestCancelStopsTheLiveBuildAndOnlyThat(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	r.reconcile()
	rec := r.records()[0]
	build := rec.State.(cv.Bootstrapping).Build
	got, err := r.svc.Cancel(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := got.State.(cv.Cancelled); !ok || c.Cause != cv.CancelUser {
		t.Fatalf("state = %#v", got.State)
	}
	if len(r.runner.Cancelled) != 1 || r.runner.Cancelled[0] != build {
		t.Fatalf("cancelled = %v", r.runner.Cancelled)
	}
	// The build's own cancelled result later changes nothing (V2).
	r.settle()
	if _, ok := r.state().(cv.Cancelled); !ok {
		t.Fatalf("state = %#v", r.state())
	}
	if _, err := r.svc.Cancel(context.Background(), rec.ID); err == nil {
		t.Fatal("cancelling a finished verification succeeded")
	} else if c, _ := apperr.ClassOf(err); c != apperr.Conflict {
		t.Fatalf("err = %v", err)
	}
}

func TestNewerTurnSupersedesAndCancelsTheOldBuild(t *testing.T) {
	r := newRig(t)
	r.runner.Hold = true
	r.reconcile()
	old := r.records()[0].State.(cv.Bootstrapping).Build
	r.store.SetReview("tsk_1", "turn_2", 2)
	r.reconcile()
	if len(r.runner.Cancelled) != 1 || r.runner.Cancelled[0] != old {
		t.Fatalf("cancelled = %v, want [%s]", r.runner.Cancelled, old)
	}
	if s := r.state(); turnSeqOf(s) != 2 {
		t.Fatalf("state = %#v", s)
	}
	// The old build's result arrives late and is ignored.
	r.runner.FinishWith(old, "pass")
	r.reconcile()
	if turnSeqOf(r.state()) != 2 {
		t.Fatalf("a stale result moved the state: %#v", r.state())
	}
}

func turnSeqOf(s cv.State) int {
	switch s := s.(type) {
	case cv.Bootstrapping:
		return s.TurnSeq
	case cv.Verifying:
		return s.TurnSeq
	case cv.Passed:
		return s.TurnSeq
	}
	return -1
}

func TestChainTooLongIsUnverifiableWithANote(t *testing.T) {
	r := newRig(t)
	var long []builds.Step
	for i := 0; i < cv.MaxSteps-1; i++ {
		long = append(long, step("s", 0))
	}
	r.tc.verify, r.tc.fullTests = long, long // FullTests append goes over the limit
	r.store.SetSettings(db.BuildSettingsRecord{Repo: "rep_1", FullTests: true, Bootstrap: true})
	r.settle()
	u, ok := r.state().(cv.Unverifiable)
	if !ok || u.Reason != cv.UnverifiableNoCommand {
		t.Fatalf("state = %#v", r.state())
	}
	if n := r.records()[0].Note; n != verify.NoteTooManySteps {
		t.Fatalf("note = %q", n)
	}
	if len(r.runner.Enqueued()) != 0 {
		t.Fatal("a build started")
	}
}

func TestBadSettingsAreADomainOutcome(t *testing.T) {
	r := newRig(t)
	r.store.SetSettings(db.BuildSettingsRecord{Repo: "rep_1", Verify: []byte(`[{"name":"x","argv":[]}]`), Bootstrap: true})
	r.settle()
	if u, ok := r.state().(cv.Unverifiable); !ok || u.Reason != cv.UnverifiableNoCommand {
		t.Fatalf("state = %#v", r.state())
	}
	if n := r.records()[0].Note; n != verify.NoteBadSettings {
		t.Fatalf("note = %q", n)
	}
}

func TestOverrideAndBootstrapOffAreUsed(t *testing.T) {
	r := newRig(t)
	r.store.SetSettings(db.BuildSettingsRecord{
		Repo: "rep_1", Verify: []byte(`[{"name":"mine","argv":["fake","exit","0"]}]`), Bootstrap: false,
	})
	r.settle()
	if _, ok := r.state().(cv.Passed); !ok {
		t.Fatalf("state = %#v", r.state())
	}
	ids := r.runner.Enqueued()
	if len(ids) != 1 {
		t.Fatalf("%d builds, want 1 (bootstrap is off)", len(ids))
	}
	st, _ := r.runner.Get(context.Background(), ids[0])
	if steps := st.(corebuilds.Finished).Spec.Steps; len(steps) != 1 || steps[0].Name != "mine" {
		t.Fatalf("steps = %+v", steps)
	}
}

func TestFactReadFailureStoresNothingAndRetries(t *testing.T) {
	r := newRig(t)
	r.probe.Err = errors.New("git is unavailable")
	r.reconcile()
	if len(r.records()) != 0 || len(r.store.Audit()) != 0 {
		t.Fatalf("something was stored after a failed read: %v", r.store.Rows())
	}
	r.probe.Err = nil
	r.settle()
	if _, ok := r.state().(cv.Passed); !ok {
		t.Fatalf("state = %#v", r.state())
	}
	// A failing store read fails the pass and writes nothing.
	r2 := newRig(t)
	r2.store.FailGet = func() error { return errors.New("db down") }
	if err := r2.svc.Reconcile(context.Background()); err == nil {
		t.Fatal("a failing store did not fail the pass")
	}
	if len(r2.store.Rows()) != 0 {
		t.Fatal("rows written")
	}
}

func TestEnqueueFailures(t *testing.T) {
	t.Run("a transient error is retried with the same ID", func(t *testing.T) {
		r := newRig(t)
		r.runner.EnqueueErr = func(n int, _ core.BuildID) error {
			if n < 3 {
				return apperr.New(apperr.Unavailable, "busy", nil)
			}
			return nil
		}
		r.settle()
		if _, ok := r.state().(cv.Passed); !ok {
			t.Fatalf("state = %#v", r.state())
		}
		if len(r.runner.Enqueued()) != 2 {
			t.Fatalf("builds = %v", r.runner.Enqueued())
		}
	})
	t.Run("a persistent error ends Unverifiable could_not_queue", func(t *testing.T) {
		r := newRig(t)
		r.runner.EnqueueErr = func(int, core.BuildID) error { return apperr.New(apperr.Unavailable, "busy", nil) }
		r.settle()
		if u, ok := r.state().(cv.Unverifiable); !ok || u.Reason != cv.UnverifiableCouldNotQueue {
			t.Fatalf("state = %#v", r.state())
		}
	})
	t.Run("a rejected spec is not retried", func(t *testing.T) {
		r := newRig(t)
		calls := 0
		r.runner.EnqueueErr = func(int, core.BuildID) error { calls++; return apperr.New(apperr.Invalid, "bad", nil) }
		r.reconcile()
		if calls != 1 {
			t.Fatalf("%d enqueue calls, want 1 (a rejected spec is not retried)", calls)
		}
		if u, ok := r.state().(cv.Unverifiable); !ok || u.Reason != cv.UnverifiableCouldNotQueue {
			t.Fatalf("state = %#v", r.state())
		}
	})
}

func TestAuditAndPublishNeverHoldSecretsOrLogs(t *testing.T) {
	r := newRig(t)
	r.tc.verify = []builds.Step{step("test", 3)}
	r.settle()
	r.runner.SetLog(r.records()[0].State.(cv.Failed).Build, 0, "token="+secret)
	for _, e := range r.store.Audit() {
		if strings.Contains(string(e.Detail), secret) || strings.Contains(e.Subject, secret) {
			t.Fatalf("audit leaks: %+v", e)
		}
	}
	for _, row := range r.store.Rows() {
		if strings.Contains(string(row.Plan), secret) || strings.Contains(row.FailText, secret) {
			t.Fatalf("row leaks: %+v", row)
		}
	}
}

func TestRunLoopReactsToWakeAndStops(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.svc.Run(ctx) }()
	deadline := make(chan struct{})
	go func() {
		defer close(deadline)
		for i := 0; i < 400; i++ {
			recs, _ := r.svc.List(context.Background(), "tsk_1")
			if len(recs) == 1 {
				if _, ok := recs[0].State.(cv.Passed); ok {
					return
				}
			}
			r.svc.Wake()
			sleep()
		}
	}()
	<-deadline
	if _, ok := r.state().(cv.Passed); !ok {
		t.Fatalf("the loop did not verify: %#v", r.state())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
