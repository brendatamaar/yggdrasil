package db

import (
	"context"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// RecordWorktree stores w with the commit it started from and its audit entry
// in the same transaction (invariant 8; WE-04). Callers use it inside
// Store.Commit; the plain CreateWorktree stays for rows without a base commit.
func (tx *Tx) RecordWorktree(ctx context.Context, w core.Worktree, baseCommit string, audit core.AuditEntry) (core.Worktree, error) {
	w, err := tx.CreateWorktree(ctx, w)
	if err != nil {
		return w, err
	}
	n, err := tx.q.SetWorktreeBaseCommit(ctx, gen.SetWorktreeBaseCommitParams{BaseCommit: baseCommit, ID: string(w.ID)})
	if err := updated("set worktree base commit "+string(w.ID), n, err); err != nil {
		return w, err
	}
	if err := tx.AppendAudit(ctx, audit); err != nil {
		return w, fmt.Errorf("record worktree: %w", err)
	}
	return w, nil
}

// GetWorktreeBaseCommit returns the commit a worktree started from ("" for rows
// made before it was recorded).
func (s *Store) GetWorktreeBaseCommit(ctx context.Context, id core.WorktreeID) (string, error) {
	c, err := s.db.reader().GetWorktreeBaseCommit(ctx, string(id))
	if err := found("get worktree base commit "+string(id), err); err != nil {
		return "", err
	}
	return c, nil
}

// markWorktree runs one monotone status update. Zero rows changed is either a
// missing worktree (ErrNotFound) or a row already past that status, which is a
// no-op: the marks are idempotent (docs/design/landing.md R8).
func (tx *Tx) markWorktree(ctx context.Context, what string, id core.WorktreeID, run func() (int64, error)) error {
	n, err := run()
	if err != nil {
		return fmt.Errorf("%s %s: %w", what, id, err)
	}
	if n > 0 {
		return nil
	}
	_, err = tx.q.GetWorktree(ctx, string(id))
	return found(what+" "+string(id), err)
}

// MarkWorktreeMerged moves active -> merged. Use it inside Store.Commit with
// the audit entry (invariant 8).
func (tx *Tx) MarkWorktreeMerged(ctx context.Context, id core.WorktreeID) error {
	return tx.markWorktree(ctx, "mark worktree merged", id, func() (int64, error) {
		return tx.q.MarkWorktreeMerged(ctx, string(id))
	})
}

// MarkWorktreeRemoving moves active/merged/removing -> removing and remembers
// the staged leaf name.
func (tx *Tx) MarkWorktreeRemoving(ctx context.Context, id core.WorktreeID, stagedLeaf string) error {
	return tx.markWorktree(ctx, "mark worktree removing", id, func() (int64, error) {
		return tx.q.MarkWorktreeRemoving(ctx, gen.MarkWorktreeRemovingParams{StagedName: stagedLeaf, ID: string(id)})
	})
}

// MarkWorktreeRemoved moves removing -> removed.
func (tx *Tx) MarkWorktreeRemoved(ctx context.Context, id core.WorktreeID) error {
	return tx.markWorktree(ctx, "mark worktree removed", id, func() (int64, error) {
		return tx.q.MarkWorktreeRemoved(ctx, string(id))
	})
}

// GetWorktreeStagedName returns the staged leaf of a removing worktree.
func (s *Store) GetWorktreeStagedName(ctx context.Context, id core.WorktreeID) (string, error) {
	n, err := s.db.reader().GetWorktreeStagedName(ctx, string(id))
	if err := found("get worktree staged name "+string(id), err); err != nil {
		return "", err
	}
	return n, nil
}
