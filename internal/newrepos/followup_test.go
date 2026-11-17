package newrepos

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func (w *world) effectOf(op core.OperationID, stage string, attempt int) db.EffectRecord {
	w.t.Helper()
	effects, err := w.store.ListEffectsByOperation(w.ctx, op)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, e := range effects {
		if e.Stage == stage && e.Attempt == attempt {
			return e
		}
	}
	w.t.Fatalf("no effect %s#%d among %+v", stage, attempt, effects)
	return db.EffectRecord{}
}

func (w *world) waitNoWorker(op core.OperationID) {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for w.svc.hasWorker(op) {
		if time.Now().After(deadline) {
			w.t.Fatal("the worker did not stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// NR-02: untracking cancels a worker whose stage may still complete or may
// already have run. Cancellation is Unknown, never failure: the in-flight
// effect is settled as unknown, a late result of the hung stage is dropped and
// does not turn it into a failure or a success, and the operation stays stopped.
func TestUntrackedWhileStageHungSettlesTheEffectAsUnknown(t *testing.T) {
	w := newWorld(t)
	w.f.on("CreateFile", 1, faultHang, nil)
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	<-w.f.entered
	if e := w.effectOf(rec.Operation.ID, "write_readme", 1); e.State != db.EffectIntent {
		t.Fatalf("in-flight effect = %s, want intent", e.State)
	}
	if err := w.svc.Untracked(w.ctx, rec.Operation.ID); err != nil {
		t.Fatal(err)
	}
	// The stop itself already settles the effect: it may not claim failure.
	if e := w.effectOf(rec.Operation.ID, "write_readme", 1); e.State != db.EffectUnknown {
		t.Fatalf("effect after untrack = %s, want unknown (the stage may still complete)", e.State)
	}
	w.waitNoWorker(rec.Operation.ID)
	// The hung stage's own (late) result arrived after the stop and was dropped.
	if e := w.effectOf(rec.Operation.ID, "write_readme", 1); e.State != db.EffectUnknown || e.ReceiptKind != "" {
		t.Fatalf("effect after the late result = %+v, want unknown without a receipt", e)
	}
	if s, ok := w.get(rec.Operation.ID).(corenewrepos.Stopped); !ok || s.Reason != corenewrepos.StopUntracked {
		t.Fatalf("state = %+v", w.get(rec.Operation.ID))
	}
	if w.f.count("CreateFile") != 1 || w.f.count("CommitFirst") != 0 {
		t.Fatalf("a stage ran after untracking: %v", w.mutations())
	}
}

// NR-02: a stop that proves the stage did not run still settles it as failed.
func TestDefiniteStopStillSettlesTheEffectAsFailed(t *testing.T) {
	w, rec := stopAtREADME(t)
	if e := w.effectOf(rec.Operation.ID, "write_readme", 1); e.State != db.EffectFailed {
		t.Fatalf("effect = %s, want failed", e.State)
	}
}

// NR-01, NR-02: a worker whose result cannot be recorded (a store error after
// the effect ran) must not leave the operation Initializing with no owner. It
// marks it Uncertain and reconciles read-only: the stage that did run is found
// again, nothing runs twice and the flow completes.
func TestFailedWorkerMarksTheOperationUncertainAndReconciles(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	// Commits after the base: 1 confirm, 2 create_directory result, 3 init_git result.
	w.sw.failAt = w.sw.commitCount() + 3
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	w.waitNoWorker(rec.Operation.ID)
	if w.f.count("CreateChildDir") != 1 || w.f.count("InitRepo") != 1 || w.f.count("CommitFirst") != 1 {
		t.Fatalf("a stage ran twice: %v", w.mutations())
	}
	w.assertFirstCommitOnDisk("shop-new")
	if !strings.Contains(w.logs.String(), "could not record a new repo step") {
		t.Fatalf("the failure was not logged:\n%s", w.logs.String())
	}
}

// The same when the worker panics inside a stage: no stuck state, no secret in
// the log, and the stage is never run again by the recovery path.
func TestPanickingWorkerMarksTheOperationUncertain(t *testing.T) {
	w := newWorld(t)
	w.f.hook("CreateFile", 2, func() { panic("boom with " + sentinelToken) }) // the .gitignore stage
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	st := w.waitSettled(rec.Operation.ID)
	// The panic happened before the file was written, so the read-only check
	// finds it absent: stopped at that stage, for a human to retry.
	s, ok := st.(corenewrepos.Stopped)
	if !ok || s.Reason != corenewrepos.StopStageFailed || s.Stage != corenewrepos.StageWriteIgnore {
		t.Fatalf("state = %T %+v", st, st)
	}
	if w.f.count("CreateFile") != 2 || w.f.count("CommitFirst") != 0 {
		t.Fatalf("recovery ran a stage: %v", w.mutations())
	}
	logs := w.logs.String()
	if !strings.Contains(logs, "panicked") {
		t.Fatalf("the panic was not logged:\n%s", logs)
	}
	if strings.Contains(logs, sentinelToken) {
		t.Fatal("a secret reached the log")
	}
}

// A failed worker whose store stays broken gives up after a bounded number of
// tries and leaves the operation for the next start; nothing runs meanwhile.
func TestFailedWorkerRecoveryIsBounded(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	w.sw.crashAt = w.sw.commitCount() + 2 // the store dies at the first result
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	w.waitNoWorker(rec.Operation.ID)
	if _, ok := w.get(rec.Operation.ID).(corenewrepos.Initializing); !ok {
		t.Fatalf("state = %T, want it left for the next start", w.get(rec.Operation.ID))
	}
	if !strings.Contains(w.logs.String(), "stays unsettled until the next start") {
		t.Fatalf("the give-up was not logged:\n%s", w.logs.String())
	}
	if w.f.count("InitRepo") != 0 {
		t.Fatalf("a stage ran after the store died: %v", w.mutations())
	}
	// The next start recovers it.
	w.restart()
	if err := w.svc.Recover(w.ctx); err != nil {
		t.Fatal(err)
	}
	w.waitSettled(rec.Operation.ID)
}

// A publish retry takes the publish proposal (the service lock) inside its
// transition. The retry must not hold that lock across it (it used to, and a
// publish retry deadlocked), and a refused push is resumed by Retry alone.
func TestPublishRetryAfterDefiniteStop(t *testing.T) {
	w := newWorld(t)
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
	w.f.on("PublishBranch", 1, faultBefore, errors.New("the host says no"))
	pv, err := w.publish(rec, "shop-new")
	if err != nil {
		t.Fatal(err)
	}
	st := w.waitSettled(rec.Operation.ID)
	s, ok := st.(corenewrepos.Stopped)
	if !ok || s.Reason != corenewrepos.StopHostRefused || s.Stage != corenewrepos.StagePushHead {
		t.Fatalf("state = %T %+v", st, st)
	}
	done := make(chan error, 1)
	go func() { done <- w.svc.Retry(w.ctx, rec.Operation.ID, pv.Hash) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Retry = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Retry did not return: it holds the service lock while it commits")
	}
	w.waitFor(rec.Operation.ID, "published", isState[corenewrepos.Published])
	if w.host.creates != 1 || w.fake.Pushes() != 1 {
		t.Fatalf("creates=%d pushes=%d: the retry repeated a finished stage", w.host.creates, w.fake.Pushes())
	}
}

// While a retry is deciding, its claim keeps every other start of the operation
// out, and a refused retry gives the claim back.
func TestRetryClaimIsReleasedWhenRefused(t *testing.T) {
	w, rec := stopAtREADME(t)
	if err := w.svc.Retry(w.ctx, rec.Operation.ID, strings.Repeat("0", 64)); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v", err)
	}
	if w.svc.hasWorker(rec.Operation.ID) {
		t.Fatal("a refused retry kept the worker slot")
	}
	if err := w.svc.Retry(w.ctx, rec.Operation.ID, rec.SpecHash); err != nil {
		t.Fatalf("the retry after a refused one = %v", err)
	}
	w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
}

// Die class: an operation that stopped on a conflicting finding runs no new
// stage, however it is confirmed.
func TestNoRetryAfterConflict(t *testing.T) {
	w := newWorld(t)
	w.f.on("CreateChildDir", 1, faultLost, errInjected) // a folder no receipt proves is ours
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	st := w.waitSettled(rec.Operation.ID)
	if s, ok := st.(corenewrepos.Stopped); !ok || s.Reason != corenewrepos.StopConflict {
		t.Fatalf("state = %T %+v", st, st)
	}
	before := w.mutations()
	err := w.svc.Retry(w.ctx, rec.Operation.ID, rec.SpecHash)
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("Retry after a conflict = %v", err)
	}
	var nw interface{ NothingWritten() bool }
	if !errors.As(err, &nw) || !nw.NothingWritten() {
		t.Fatalf("the refusal must say nothing was written: %v", err)
	}
	for op, n := range w.mutations() {
		if n != before[op] {
			t.Fatalf("%s ran after a refused retry", op)
		}
	}
}
