package newrepos

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// stopAtREADME runs an init whose README stage stops definitely (the folder
// "changed"), and returns the stopped operation.
func stopAtREADME(t *testing.T) (*world, db.ReservationRecord) {
	t.Helper()
	w := newWorld(t)
	w.f.on("CreateFile", 1, faultBefore, platform.ErrChanged)
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	st := w.waitSettled(rec.Operation.ID)
	s, ok := st.(corenewrepos.Stopped)
	if !ok || s.Reason != corenewrepos.StopPreconditions || s.Attempt != 1 || s.Stage != corenewrepos.StageWriteREADME {
		t.Fatalf("state = %+v", st)
	}
	return w, rec
}

// A retry after a definite stop is a fresh confirmation with a greater attempt;
// it resumes at the parked stage and never repeats the earlier ones.
func TestRetryAfterDefiniteStop(t *testing.T) {
	w, rec := stopAtREADME(t)
	if err := w.svc.Retry(w.ctx, rec.Operation.ID, rec.SpecHash); err != nil {
		t.Fatal(err)
	}
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	if w.f.count("CreateChildDir") != 1 || w.f.count("InitRepo") != 1 || w.f.count("CommitFirst") != 1 || w.f.count("CreateFile") != 3 {
		t.Fatalf("the retry repeated a finished stage: %v", w.mutations())
	}
	effects, _ := w.store.ListEffectsByOperation(w.ctx, rec.Operation.ID)
	var attempts []int
	for _, e := range effects {
		if e.Stage == "write_readme" {
			attempts = append(attempts, e.Attempt)
		}
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("write_readme attempts = %v, want [1 2]", attempts)
	}
	if got, _ := w.readFile("shop-new", "README.md"); got != "# shop-new\r\nline two\r\n" {
		t.Fatalf("README = %q", got)
	}
}

// A retry is refused, and nothing runs, when the preconditions no longer hold.
func TestRetryRefusedWhenPreconditionsFail(t *testing.T) {
	w, rec := stopAtREADME(t)
	// Someone else wrote the file meanwhile: it must be inspected, not replaced.
	if err := os.WriteFile(filepath.Join(w.dir("shop-new"), "README.md"), []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := w.f.count("CreateFile")
	err := w.svc.Retry(w.ctx, rec.Operation.ID, rec.SpecHash)
	var ref *RefusedError
	if !errors.As(err, &ref) || ref.Reason != corenewrepos.StopPreconditions {
		t.Fatalf("err = %v", err)
	}
	if w.f.count("CreateFile") != before {
		t.Fatal("a stage ran although the preconditions failed")
	}
	if got, _ := w.readFile("shop-new", "README.md"); got != "theirs\n" {
		t.Fatalf("README = %q", got)
	}
	if s, ok := w.get(rec.Operation.ID).(corenewrepos.Stopped); !ok || s.Attempt != 1 {
		t.Fatalf("state changed: %+v", w.get(rec.Operation.ID))
	}
}

// A rejected or untracked operation never retries; a wrong hash or the wrong
// state is refused.
func TestRetryNeverForRejectedOrUntracked(t *testing.T) {
	w := newWorld(t)
	a := w.reserveAndPropose("shop-a")
	if err := w.svc.Reject(w.ctx, a.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Retry(w.ctx, a.Operation.ID, a.SpecHash); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("rejected: %v", err)
	}
	b := w.reserveAndPropose("shop-b")
	if err := w.svc.Untracked(w.ctx, b.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Retry(w.ctx, b.Operation.ID, b.SpecHash); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("untracked: %v", err)
	}
	c := w.reserveAndPropose("shop-c")
	if err := w.svc.Retry(w.ctx, c.Operation.ID, c.SpecHash); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("awaiting confirm: %v", err)
	}
	if w.anyMutation() {
		t.Fatalf("mutating port called: %v", w.mutations())
	}
}

// Without a fresh Retry confirmation nothing repeats a stopped stage, however
// often the service restarts or recovers, and a retry with another hash is refused.
func TestRetryNeverAutomatic(t *testing.T) {
	w, rec := stopAtREADME(t)
	for i := 0; i < 3; i++ {
		w.restart()
		_ = w.svc.Recover(w.ctx)
		_ = w.svc.ReconcileOperation(w.ctx, rec.Operation.ID)
		w.waitSettled(rec.Operation.ID)
	}
	if err := w.svc.Retry(w.ctx, rec.Operation.ID, strings.Repeat("0", 64)); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v", err)
	}
	if w.f.count("CreateFile") != 1 {
		t.Fatalf("a stopped stage ran again without a retry: %v", w.mutations())
	}
	if _, ok := w.get(rec.Operation.ID).(corenewrepos.Stopped); !ok {
		t.Fatalf("state = %T", w.get(rec.Operation.ID))
	}
}

// An Uncertain stage found absent is retried only by a fresh confirmation too.
func TestRetryAfterUnknownThenAbsent(t *testing.T) {
	w := newWorld(t)
	w.f.on("CommitFirst", 1, faultBefore, errInjected) // unknown, effect absent
	rec := w.reserveAndPropose("shop-new")
	_ = w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1)
	if s, ok := w.waitSettled(rec.Operation.ID).(corenewrepos.Stopped); !ok || s.Stage != corenewrepos.StageFirstCommit {
		t.Fatalf("state = %+v", w.get(rec.Operation.ID))
	}
	if err := w.svc.Retry(w.ctx, rec.Operation.ID, rec.SpecHash); err != nil {
		t.Fatal(err)
	}
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	if w.f.count("CommitFirst") != 2 || w.f.count("CreateFile") != 2 {
		t.Fatalf("mutations = %v", w.mutations())
	}
	w.assertFirstCommitOnDisk("shop-new")
}

// Untracking while a worker runs cancels the worker, stops the operation, runs
// no further stage and deletes nothing.
func TestUntrackedWhileWorkerRuns(t *testing.T) {
	w := newWorld(t)
	w.f.on("CreateFile", 1, faultHang, nil)
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	<-w.f.entered
	if err := w.svc.Untracked(w.ctx, rec.Operation.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for w.svc.hasWorker(rec.Operation.ID) {
		if time.Now().After(deadline) {
			t.Fatal("the worker was not cancelled")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s, ok := w.get(rec.Operation.ID).(corenewrepos.Stopped)
	if !ok || s.Reason != corenewrepos.StopUntracked || s.Stage != corenewrepos.StageWriteREADME {
		t.Fatalf("state = %+v", w.get(rec.Operation.ID))
	}
	if w.f.count("CreateFile") != 1 || w.f.count("CommitFirst") != 0 {
		t.Fatalf("a stage ran after untracking: %v", w.mutations())
	}
	// Nothing deleted: the folder and the .git folder the earlier stages made stay.
	if _, err := os.Stat(filepath.Join(w.dir("shop-new"), ".git")); err != nil {
		t.Fatalf("the repo folder was touched: %v", err)
	}
	if err := w.svc.Retry(w.ctx, rec.Operation.ID, rec.SpecHash); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("an untracked operation must not retry: %v", err)
	}
}
