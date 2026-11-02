package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestRecordWorktreeWritesRowBaseAndAuditTogether(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	w := core.Worktree{ID: "wt_1", RepoID: r.ID, TaskID: task.ID, Name: "calm-pine", Path: `C:\y\wt\calm-pine`, Branch: "ygg/calm-pine", CreatedAt: at}
	audit := core.AuditEntry{ID: "aud_1", At: at, Actor: "system", Action: "worktree.create", Subject: "task:" + string(task.ID)}

	// An invalid audit entry rolls the worktree back too.
	bad := audit
	bad.Actor = ""
	err = s.Commit(ctx, func(tx *Tx) error { _, err := tx.RecordWorktree(ctx, w, "abc123", bad); return err })
	if err == nil {
		t.Fatal("want an error for an audit entry without an actor")
	}
	if ws, _ := s.ListWorktrees(ctx, task.ID); len(ws) != 0 {
		t.Fatalf("worktree stored without its audit entry: %+v", ws)
	}

	if err := s.Commit(ctx, func(tx *Tx) error { _, err := tx.RecordWorktree(ctx, w, "abc123", audit); return err }); err != nil {
		t.Fatal(err)
	}
	if base, err := s.GetWorktreeBaseCommit(ctx, w.ID); err != nil || base != "abc123" {
		t.Fatalf("base = %q, %v", base, err)
	}
	if rows, err := s.ListAudit(ctx, audit.Subject, 10); err != nil || len(rows) != 1 {
		t.Fatalf("audit = %v, %v", rows, err)
	}
	if _, err := s.GetWorktreeBaseCommit(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing worktree: %v", err)
	}
}

func TestWorktreeMarksAreMonotoneIdempotentAndAtomic(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	task, err := s.CreateTask(ctx, newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	w := core.Worktree{ID: "wt_m", RepoID: r.ID, TaskID: task.ID, Name: "calm-pine", Path: `C:\y\wt\calm-pine`, Branch: "ygg/calm-pine", CreatedAt: at}
	audit := core.AuditEntry{ID: "aud_m", At: at, Actor: "system", Action: "worktree.create", Subject: "task:" + string(task.ID)}
	if err := s.Commit(ctx, func(tx *Tx) error { _, err := tx.RecordWorktree(ctx, w, "abc", audit); return err }); err != nil {
		t.Fatal(err)
	}
	status := func() core.WorktreeStatus {
		got, err := s.GetWorktree(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Status
	}
	do := func(f func(tx *Tx) error) error { return s.Commit(ctx, f) }

	// removed before removing is a no-op, not an error.
	if err := do(func(tx *Tx) error { return tx.MarkWorktreeRemoved(ctx, w.ID) }); err != nil || status() != core.WorktreeActive {
		t.Fatalf("removed from active: %v, %s", err, status())
	}
	if err := do(func(tx *Tx) error { return tx.MarkWorktreeMerged(ctx, w.ID) }); err != nil || status() != core.WorktreeMerged {
		t.Fatalf("merged: %v, %s", err, status())
	}
	if err := do(func(tx *Tx) error { return tx.MarkWorktreeMerged(ctx, w.ID) }); err != nil {
		t.Fatalf("merged twice: %v", err)
	}
	if err := do(func(tx *Tx) error { return tx.MarkWorktreeRemoving(ctx, w.ID, "stage_1") }); err != nil || status() != core.WorktreeRemoving {
		t.Fatalf("removing: %v, %s", err, status())
	}
	if n, err := s.GetWorktreeStagedName(ctx, w.ID); err != nil || n != "stage_1" {
		t.Fatalf("staged = %q, %v", n, err)
	}
	// merged never moves backwards.
	if err := do(func(tx *Tx) error { return tx.MarkWorktreeMerged(ctx, w.ID) }); err != nil || status() != core.WorktreeRemoving {
		t.Fatalf("merged after removing: %v, %s", err, status())
	}
	// A failure after the mark rolls it back.
	boom := errors.New("boom")
	if err := do(func(tx *Tx) error {
		if err := tx.MarkWorktreeRemoved(ctx, w.ID); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) || status() != core.WorktreeRemoving {
		t.Fatalf("rollback: %v, %s", err, status())
	}
	if err := do(func(tx *Tx) error { return tx.MarkWorktreeRemoved(ctx, w.ID) }); err != nil || status() != core.WorktreeRemoved {
		t.Fatalf("removed: %v, %s", err, status())
	}
	for _, f := range []func(tx *Tx) error{
		func(tx *Tx) error { return tx.MarkWorktreeMerged(ctx, "wt_none") },
		func(tx *Tx) error { return tx.MarkWorktreeRemoving(ctx, "wt_none", "x") },
		func(tx *Tx) error { return tx.MarkWorktreeRemoved(ctx, "wt_none") },
	} {
		if err := do(f); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing worktree: %v", err)
		}
	}
}
