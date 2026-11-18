package newrepos

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// expectation of a settled operation after a crash.
type outcome struct {
	ready     bool
	stopped   corenewrepos.StopReason // when not ready
	published bool
}

func (w *world) assertOutcome(op string, st corenewrepos.State, want outcome) {
	w.t.Helper()
	switch {
	case want.published:
		if _, ok := st.(corenewrepos.Published); !ok {
			w.t.Fatalf("%s: state = %T %+v, want Published", op, st, st)
		}
	case want.ready:
		if _, ok := st.(corenewrepos.Ready); !ok {
			w.t.Fatalf("%s: state = %T %+v, want Ready", op, st, st)
		}
	default:
		s, ok := st.(corenewrepos.Stopped)
		if !ok || s.Reason != want.stopped {
			w.t.Fatalf("%s: state = %T %+v, want Stopped(%s)", op, st, st, want.stopped)
		}
	}
}

// exactlyOnce asserts that no non-idempotent effect ever ran twice.
func (w *world) exactlyOnce(label string) {
	w.t.Helper()
	max := map[string]int{"CreateChildDir": 1, "CreateFile": 2, "InitRepo": 1, "CommitFirst": 1, "AddOrigin": 1, "CreateRepo": 1, "PublishBranch": 1}
	for op, n := range w.mutations() {
		if n > max[op] {
			w.t.Errorf("%s: %s ran %d times (at most %d)", label, op, n, max[op])
		}
	}
}

func (w *world) assertFirstCommitOnDisk(leaf string) {
	w.t.Helper()
	fc, found, err := w.git.ReadFirstCommit(w.ctx, w.dir(leaf), "main")
	if err != nil || !found || fc.Tree != expectedTree(mustSpec(w.t, w, leaf)) {
		w.t.Fatalf("the first commit is not exactly the confirmed one: %+v %v %v", fc, found, err)
	}
}

// NR-01, NR-02: TestNewRepoCrashAtEveryStage kills the flow at every stage
// boundary (a crash between an effect and its record, a lost reply, a cancel
// during the effect), restarts through Recover and asserts exactly-once effects.
func TestNewRepoCrashAtEveryStage(t *testing.T) {
	// A lost reply: the effect happened, the caller never heard. The worker
	// reconciles read-only and carries on; nothing runs twice.
	lost := []struct {
		op   string
		nth  int
		want outcome
	}{
		// No identity receipt exists for an unknown folder create: a folder that
		// is there is never adopted, a human has to look.
		{"CreateChildDir", 1, outcome{stopped: corenewrepos.StopConflict}},
		{"InitRepo", 1, outcome{ready: true}},
		{"CreateFile", 1, outcome{ready: true}},
		{"CreateFile", 2, outcome{ready: true}},
		{"CommitFirst", 1, outcome{ready: true}},
	}
	for _, c := range lost {
		t.Run(fmt.Sprintf("lost reply %s#%d", c.op, c.nth), func(t *testing.T) {
			w := newWorld(t)
			w.f.on(c.op, c.nth, faultLost, errInjected)
			rec := w.reserveAndPropose("shop-new")
			if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
				t.Fatal(err)
			}
			st := w.waitSettled(rec.Operation.ID)
			w.assertOutcome(c.op, st, c.want)
			// More recovery passes never repeat an effect.
			w.restart()
			_ = w.svc.Recover(w.ctx)
			_ = w.svc.ReconcileOperation(w.ctx, rec.Operation.ID)
			w.assertOutcome(c.op+" after restart", w.waitSettled(rec.Operation.ID), c.want)
			w.exactlyOnce(c.op)
			if c.want.ready {
				w.assertFirstCommitOnDisk("shop-new")
			}
		})
	}

	// A failure before the effect with an unknown class: reconciliation finds
	// the effect absent and the operation stops; nothing is retried by itself.
	for _, c := range []struct {
		op  string
		nth int
	}{{"CreateChildDir", 1}, {"InitRepo", 1}, {"CreateFile", 1}, {"CreateFile", 2}, {"CommitFirst", 1}} {
		t.Run(fmt.Sprintf("failed before %s#%d", c.op, c.nth), func(t *testing.T) {
			w := newWorld(t)
			w.f.on(c.op, c.nth, faultBefore, errInjected)
			rec := w.reserveAndPropose("shop-new")
			if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
				t.Fatal(err)
			}
			st := w.waitSettled(rec.Operation.ID)
			w.assertOutcome(c.op, st, outcome{stopped: corenewrepos.StopStageFailed})
			w.restart()
			_ = w.svc.Recover(w.ctx)
			w.assertOutcome(c.op+" after restart", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopStageFailed})
			w.exactlyOnce(c.op)
			if got := w.f.count(c.op); got != c.nth {
				t.Errorf("%s ran %d times; nothing may retry an unknown outcome by itself", c.op, got)
			}
		})
	}

	// A crash between an effect and the commit that records it: the process
	// dies, the next start finds the intent, marks the stage Uncertain and
	// reconciles. Commits: 1 reserve, 2 attach, 3 confirm, 4..9 the six stages.
	crashWant := map[int]outcome{
		4: {stopped: corenewrepos.StopConflict}, // folder created, identity never recorded
		5: {ready: true}, 6: {ready: true}, 7: {ready: true}, 8: {ready: true},
		9: {stopped: corenewrepos.StopStageFailed}, // finalize is the commit itself: it did not happen
	}
	for k := 4; k <= 9; k++ {
		t.Run(fmt.Sprintf("crash before commit %d", k), func(t *testing.T) {
			w := newWorld(t)
			w.sw.crashAt = k
			rec := w.reserveAndPropose("shop-new")
			if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
				t.Fatal(err)
			}
			// The worker stops at the simulated crash; "kill" the process.
			deadline := 0
			for w.sw.commitCount() < k && deadline < 2000 {
				deadline++
				w.waitTick()
			}
			_ = w.svc.Close()
			if _, ok := w.get(rec.Operation.ID).(corenewrepos.Ready); ok {
				t.Fatal("the crash did not stop the flow")
			}
			w.restart()
			if err := w.svc.Recover(w.ctx); err != nil {
				t.Fatal(err)
			}
			st := w.waitSettled(rec.Operation.ID)
			w.assertOutcome(fmt.Sprintf("commit %d", k), st, crashWant[k])
			w.exactlyOnce(fmt.Sprintf("commit %d", k))
			if crashWant[k].ready {
				w.assertFirstCommitOnDisk("shop-new")
				if _, err := w.store.GetRepo(w.ctx, rec.Operation.Repo); err != nil {
					t.Fatalf("a recovered repo must be ready: %v", err)
				}
				if got, _ := w.readFile("shop-new", "README.md"); got != "# shop-new\r\nline two\r\n" {
					t.Fatalf("README = %q", got)
				}
			}
		})
	}
}

func (w *world) waitTick() { <-timeAfter() }

// Cancelling during a mutating stage is Uncertain (never failed, never rolled
// back); the next start reconciles read-only.
func TestCancelDuringStageIsUncertain(t *testing.T) {
	w := newWorld(t)
	w.f.on("CreateFile", 1, faultHang, nil)
	rec := w.reserveAndPropose("shop-new")
	if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
		t.Fatal(err)
	}
	if op := <-w.f.entered; op != "CreateFile" {
		t.Fatalf("entered %s", op)
	}
	if err := w.svc.Close(); err != nil {
		t.Fatal(err)
	}
	u, ok := w.get(rec.Operation.ID).(corenewrepos.Uncertain)
	if !ok || u.Stage != corenewrepos.StageWriteREADME {
		t.Fatalf("state = %T %+v, want Uncertain(write_readme)", w.get(rec.Operation.ID), w.get(rec.Operation.ID))
	}
	if _, err := os.Stat(filepath.Join(w.dir("shop-new"), "README.md")); err == nil {
		t.Fatal("the cancelled stage must not have written")
	}
	w.restart()
	if err := w.svc.Recover(w.ctx); err != nil {
		t.Fatal(err)
	}
	w.assertOutcome("after restart", w.waitSettled(rec.Operation.ID), outcome{stopped: corenewrepos.StopStageFailed})
	if w.f.count("CreateFile") != 1 || w.f.count("InitRepo") != 1 {
		t.Fatalf("recovery repeated a stage: %v", w.mutations())
	}
	effects, _ := w.store.ListEffectsByOperation(w.ctx, rec.Operation.ID)
	last := effects[len(effects)-1]
	if last.Stage != "write_readme" || last.State != db.EffectFailed {
		t.Fatalf("the cancelled effect must end failed after an absent reconcile, got %+v", last)
	}
}

// publishCrashWant: every publish stage is provable by a read-only check, so a
// crash anywhere ends Published with one create, one origin, one push.
func TestNewRepoCrashAtEveryPublishStage(t *testing.T) {
	setup := func(t *testing.T) (*world, db.ReservationRecord) {
		w := newWorld(t)
		rec := w.reserveAndPropose("shop-new")
		if err := w.svc.ConfirmInit(w.ctx, rec.Operation.ID, rec.SpecHash, 1); err != nil {
			t.Fatal(err)
		}
		w.waitFor(rec.Operation.ID, "ready", isState[corenewrepos.Ready])
		return w, rec
	}
	for _, c := range []struct {
		op  string
		err error
	}{{"CreateRepo", errInjected}, {"AddOrigin", errInjected}, {"PublishBranch", git.ErrPublishUnknown}} {
		t.Run("lost reply "+c.op, func(t *testing.T) {
			w, rec := setup(t)
			w.f.on(c.op, 1, faultLost, c.err)
			if _, err := w.publish(rec, "shop-new"); err != nil {
				t.Fatal(err)
			}
			w.assertOutcome(c.op, w.waitSettled(rec.Operation.ID), outcome{published: true})
			w.restart()
			_ = w.svc.Recover(w.ctx)
			w.assertOutcome(c.op+" after restart", w.waitSettled(rec.Operation.ID), outcome{published: true})
			w.exactlyOnce(c.op)
			if w.host.creates != 1 || w.fake.Pushes() != 1 {
				t.Fatalf("creates=%d pushes=%d", w.host.creates, w.fake.Pushes())
			}
		})
	}
	// Commits of a publish: 1 confirm, then create_remote, add_origin, push_head.
	for j := 1; j <= 3; j++ {
		t.Run(fmt.Sprintf("crash before publish commit %d", j), func(t *testing.T) {
			w, rec := setup(t)
			base := w.sw.commitCount()
			w.sw.crashAt = base + 1 + j
			if _, err := w.publish(rec, "shop-new"); err != nil {
				t.Fatal(err)
			}
			for i := 0; w.sw.commitCount() < base+1+j && i < 2000; i++ {
				w.waitTick()
			}
			_ = w.svc.Close()
			if _, ok := w.get(rec.Operation.ID).(corenewrepos.Published); ok {
				t.Fatal("the crash did not stop the flow")
			}
			w.restart()
			if err := w.svc.Recover(w.ctx); err != nil {
				t.Fatal(err)
			}
			w.assertOutcome(fmt.Sprintf("commit %d", j), w.waitSettled(rec.Operation.ID), outcome{published: true})
			w.exactlyOnce("publish")
			if w.host.creates != 1 || w.fake.Pushes() != 1 {
				t.Fatalf("creates=%d pushes=%d", w.host.creates, w.fake.Pushes())
			}
		})
	}
}
