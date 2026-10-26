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
