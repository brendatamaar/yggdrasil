package verifytest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	auditpkg "github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

// RunnerHarness is a verify.BuildRunner under test with the names it knows.
type RunnerHarness struct {
	Runner   verify.BuildRunner
	Repo     core.RepoID
	Worktree core.WorktreeID
	// WaitFinished blocks until the build finished and returns its state.
	WaitFinished func(t *testing.T, id core.BuildID) corebuilds.State
}

func (h RunnerHarness) spec(argvs ...[]string) corebuilds.Spec {
	s := corebuilds.Spec{Target: corebuilds.InWorktree{WorktreeID: h.Worktree}}
	for i, a := range argvs {
		s.Steps = append(s.Steps, corebuilds.StepSpec{RepoID: h.Repo, Name: fmt.Sprintf("step %d", i+1), Argv: a})
	}
	return s
}

func exitArgv(code int) []string { return []string{"fake", "exit", fmt.Sprint(code)} }

// BuildRunnerContract is the suite every verify.BuildRunner must pass (fake
// and real builds service): enqueue is idempotent by ID, an unknown build is
// NotFound, cancelling a finished build is a no-op, and a finished build's
// result maps to exactly one well-formed BuildResult every time it is read
// (at-least-once delivery).
func BuildRunnerContract(t *testing.T, newRunner func(t *testing.T) RunnerHarness) {
	ctx := context.Background()
	t.Run("enqueue twice with one ID gives one build", func(t *testing.T) {
		h := newRunner(t)
		first := h.spec(exitArgv(0))
		second := h.spec(exitArgv(1), exitArgv(0))
		id := core.BuildID("bld_contract_1")
		got, err := h.Runner.EnqueueWithID(ctx, id, first, "system")
		if err != nil || got != id {
			t.Fatalf("first enqueue: %v %v", got, err)
		}
		got, err = h.Runner.EnqueueWithID(ctx, id, second, "system")
		if err != nil || got != id {
			t.Fatalf("second enqueue: %v %v", got, err)
		}
		fin, ok := h.WaitFinished(t, id).(corebuilds.Finished)
		if !ok {
			t.Fatal("build did not finish")
		}
		if len(fin.Spec.Steps) != 1 {
			t.Fatalf("the second spec replaced the first: %d steps", len(fin.Spec.Steps))
		}
		if _, ok := fin.Result.(corebuilds.ResultSucceeded); !ok {
			t.Fatalf("result %#v", fin.Result)
		}
	})
	t.Run("enqueue after the build finished returns it and starts no second", func(t *testing.T) {
		h := newRunner(t)
		id := core.BuildID("bld_contract_2")
		if _, err := h.Runner.EnqueueWithID(ctx, id, h.spec(exitArgv(0)), "system"); err != nil {
			t.Fatal(err)
		}
		h.WaitFinished(t, id)
		if _, err := h.Runner.EnqueueWithID(ctx, id, h.spec(exitArgv(3)), "system"); err != nil {
			t.Fatal(err)
		}
		fin, _ := h.WaitFinished(t, id).(corebuilds.Finished)
		if _, ok := fin.Result.(corebuilds.ResultSucceeded); !ok {
			t.Fatalf("a finished build was replaced: %#v", fin.Result)
		}
	})
	t.Run("get of an unknown build is not found", func(t *testing.T) {
		h := newRunner(t)
		_, err := h.Runner.Get(ctx, "bld_nope")
		if err == nil || !isNotFound(err) {
			t.Fatalf("err = %v, want not found", err)
		}
	})
	t.Run("an invalid ID or spec is refused and nothing is stored", func(t *testing.T) {
		h := newRunner(t)
		if _, err := h.Runner.EnqueueWithID(ctx, "bld bad id", h.spec(exitArgv(0)), "system"); err == nil {
			t.Error("a bad ID was accepted")
		}
		if _, err := h.Runner.EnqueueWithID(ctx, "bld_contract_3", corebuilds.Spec{Target: corebuilds.InWorktree{WorktreeID: h.Worktree}}, "system"); err == nil {
			t.Error("a spec without steps was accepted")
		}
		if _, err := h.Runner.Get(ctx, "bld_contract_3"); err == nil || !isNotFound(err) {
			t.Errorf("a refused build exists: %v", err)
		}
	})
	t.Run("cancel of a finished build is a no-op", func(t *testing.T) {
		h := newRunner(t)
		id := core.BuildID("bld_contract_4")
		if _, err := h.Runner.EnqueueWithID(ctx, id, h.spec(exitArgv(0)), "system"); err != nil {
			t.Fatal(err)
		}
		h.WaitFinished(t, id)
		if err := h.Runner.Cancel(ctx, id, "system"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		fin, _ := h.WaitFinished(t, id).(corebuilds.Finished)
		if _, ok := fin.Result.(corebuilds.ResultSucceeded); !ok {
			t.Fatalf("cancel changed a finished build: %#v", fin.Result)
		}
	})
	t.Run("a finished build maps to exactly one result, every time", func(t *testing.T) {
		h := newRunner(t)
		cases := []struct {
			name string
			spec corebuilds.Spec
			want cv.BuildResult
		}{
			{"passed", h.spec(exitArgv(0), exitArgv(0)), cv.BuildPassed{}},
			{"failed at the second step", h.spec(exitArgv(0), exitArgv(3), exitArgv(0)), cv.BuildFailedAt{Step: 1, ExitCode: 3}},
		}
		for i, c := range cases {
			id := core.BuildID(fmt.Sprintf("bld_contract_map_%d", i))
			if _, err := h.Runner.EnqueueWithID(ctx, id, c.spec, "system"); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			st := h.WaitFinished(t, id)
			for read := 0; read < 3; read++ {
				got, ok := verify.ToBuildResult(len(c.spec.Steps), st)
				if !ok || got != c.want {
					t.Fatalf("%s read %d: got %#v ok=%v, want %#v", c.name, read, got, ok, c.want)
				}
				again, err := h.Runner.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				st = again
			}
		}
	})
}

func isNotFound(err error) bool {
	if errors.Is(err, db.ErrNotFound) {
		return true
	}
	c, ok := apperr.ClassOf(err)
	return ok && c == apperr.NotFound
}

// StoreHarness is a verify.Store under test with seeded names: a task with
// two active worktrees (Repos[i] in Worktrees[i]) and one merged worktree.
type StoreHarness struct {
	Store          verify.Store
	Task           core.TaskID
	Repos          [2]core.RepoID
	Worktrees      [2]core.WorktreeID
	MergedRepo     core.RepoID
	MergedWorktree core.WorktreeID
	// Audit returns the audit entries stored for a subject.
	Audit func(subject string) []core.AuditEntry
}

var contractNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func (h StoreHarness) row(t *testing.T, i int, st cv.State, id string) db.VerificationRow {
	t.Helper()
	row, err := verify.RowFromVerification(cv.Verification{
		ID: cv.VerificationID(id), Task: h.Task, Repo: h.Repos[i], Worktree: h.Worktrees[i], State: st,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func passed(seq int) cv.State {
	return cv.Passed{Turn: "turn_1", TurnSeq: seq, Attempt: 1, Build: "bld_1", At: contractNow}
}

func verifying() cv.State {
	return cv.Verifying{
		Turn: "turn_1", TurnSeq: 1, Attempt: 1, Build: "bld_2", Since: contractNow,
		Plan: cv.Plan{Verify: []cv.StepSpec{{Name: "test", Argv: []string{"go", "test"}}}},
	}
}

// Contract entries are fixtures written straight to the store under test; the
// service's own entries go through audit.Prepare.
var _ = auditpkg.Prepare

func audit(id, subject string) core.AuditEntry {
	return core.AuditEntry{ID: core.AuditID(id), At: contractNow, Actor: "system", Action: "verify.test", Subject: subject}
}

// VerifyStoreContract is the suite every verify.Store must pass (fake and
// SQLite): versions, a unique row per (task, repo), the audit entry written in
// the same transaction as the state, and the list queries.
func VerifyStoreContract(t *testing.T, newStore func(t *testing.T) StoreHarness) {
	ctx := context.Background()
	save := func(h StoreHarness, row db.VerificationRow, expected int64, extra ...core.AuditEntry) (ver int64, err error) {
		err = h.Store.Commit(ctx, func(tx verify.Tx) error {
			v, e := tx.Save(ctx, row, expected, contractNow)
			if e != nil {
				return e
			}
			ver = v
			for _, a := range extra {
				if e := tx.AppendAudit(ctx, a); e != nil {
					return e
				}
			}
			return nil
		})
		return ver, err
	}
	t.Run("insert, read back, update bumps the version", func(t *testing.T) {
		h := newStore(t)
		row := h.row(t, 0, passed(1), "ver_a")
		if v, err := save(h, row, db.NewVerification); err != nil || v != 0 {
			t.Fatalf("insert: %d %v", v, err)
		}
		got, err := h.Store.GetByTaskRepo(ctx, h.Task, h.Repos[0])
		if err != nil || got.ID != "ver_a" || got.Version != 0 || got.State != "passed" {
			t.Fatalf("read back: %+v %v", got, err)
		}
		if _, err := verify.VerificationFromRow(got); err != nil {
			t.Fatalf("the stored row does not parse: %v", err)
		}
		next := h.row(t, 0, verifying(), "ver_a")
		if v, err := save(h, next, 0); err != nil || v != 1 {
			t.Fatalf("update: %d %v", v, err)
		}
		byID, err := h.Store.Get(ctx, "ver_a")
		if err != nil || byID.Version != 1 || byID.State != "verifying" || byID.Build != "bld_2" {
			t.Fatalf("after update: %+v %v", byID, err)
		}
	})
	t.Run("a stale version is a conflict and changes nothing", func(t *testing.T) {
		h := newStore(t)
		if _, err := save(h, h.row(t, 0, passed(1), "ver_a"), db.NewVerification); err != nil {
			t.Fatal(err)
		}
		if _, err := save(h, h.row(t, 0, verifying(), "ver_a"), 0); err != nil {
			t.Fatal(err)
		}
		_, err := save(h, h.row(t, 0, passed(2), "ver_a"), 0)
		if !errors.Is(err, db.ErrConflict) {
			t.Fatalf("err = %v, want conflict", err)
		}
		got, _ := h.Store.Get(ctx, "ver_a")
		if got.State != "verifying" || got.Version != 1 {
			t.Fatalf("a refused write changed the row: %+v", got)
		}
	})
	t.Run("a second row for one task and repo is a conflict", func(t *testing.T) {
		h := newStore(t)
		if _, err := save(h, h.row(t, 0, passed(1), "ver_a"), db.NewVerification); err != nil {
			t.Fatal(err)
		}
		if _, err := save(h, h.row(t, 0, passed(1), "ver_b"), db.NewVerification); !errors.Is(err, db.ErrConflict) {
			t.Fatalf("err = %v, want conflict", err)
		}
	})
	t.Run("updating a missing row is not found", func(t *testing.T) {
		h := newStore(t)
		if _, err := save(h, h.row(t, 0, passed(1), "ver_missing"), 0); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("err = %v, want not found", err)
		}
		if _, err := h.Store.Get(ctx, "ver_missing"); !errors.Is(err, db.ErrNotFound) && !isNotFound(err) {
			t.Fatalf("get of a missing row: %v", err)
		}
	})
	t.Run("state and audit are written in one transaction", func(t *testing.T) {
		h := newStore(t)
		subject := "verification:ver_a"
		if _, err := save(h, h.row(t, 0, passed(1), "ver_a"), db.NewVerification, audit("aud_1", subject)); err != nil {
			t.Fatal(err)
		}
		if n := len(h.Audit(subject)); n != 1 {
			t.Fatalf("%d audit entries after a commit, want 1", n)
		}
		// A failing audit entry rolls the state back.
		bad := audit("aud_2", subject)
		bad.Action = ""
		if _, err := save(h, h.row(t, 0, verifying(), "ver_a"), 0, bad); err == nil {
			t.Fatal("a commit with an invalid audit entry succeeded")
		}
		got, _ := h.Store.Get(ctx, "ver_a")
		if got.State != "passed" || got.Version != 0 {
			t.Fatalf("the state moved although its audit entry failed: %+v", got)
		}
		if n := len(h.Audit(subject)); n != 1 {
			t.Fatalf("%d audit entries after a failed commit, want 1", n)
		}
		// And the other way round: a failing state write drops the entry.
		if _, err := save(h, h.row(t, 0, verifying(), "ver_a"), 7, audit("aud_3", subject)); err == nil {
			t.Fatal("a stale write succeeded")
		}
		if n := len(h.Audit(subject)); n != 1 {
			t.Fatalf("an audit entry survived a failed state write: %d", n)
		}
	})
	t.Run("lists return the right rows", func(t *testing.T) {
		h := newStore(t)
		if _, err := save(h, h.row(t, 0, verifying(), "ver_a"), db.NewVerification); err != nil {
			t.Fatal(err)
		}
		interrupted := cv.Interrupted{Turn: "turn_1", TurnSeq: 1, Attempt: 1, Stage: cv.StageVerify, At: contractNow}
		if _, err := save(h, h.row(t, 1, interrupted, "ver_b"), db.NewVerification); err != nil {
			t.Fatal(err)
		}
		live, err := h.Store.ListLive(ctx)
		if err != nil || len(live) != 1 || live[0].ID != "ver_a" {
			t.Fatalf("live: %+v %v", live, err)
		}
		inter, err := h.Store.ListInterrupted(ctx)
		if err != nil || len(inter) != 1 || inter[0].ID != "ver_b" {
			t.Fatalf("interrupted: %+v %v", inter, err)
		}
		all, err := h.Store.ListByTask(ctx, h.Task)
		if err != nil || len(all) != 2 || all[0].Repo > all[1].Repo {
			t.Fatalf("by task: %+v %v", all, err)
		}
	})
	t.Run("worktrees lists only active ones", func(t *testing.T) {
		h := newStore(t)
		wts, err := h.Store.Worktrees(ctx, h.Task)
		if err != nil {
			t.Fatal(err)
		}
		got := map[core.WorktreeID]bool{}
		for _, w := range wts {
			got[w.ID] = true
		}
		if len(got) != 2 || !got[h.Worktrees[0]] || !got[h.Worktrees[1]] || got[h.MergedWorktree] {
			t.Fatalf("worktrees = %v", got)
		}
	})
}
