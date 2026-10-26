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

// WorktreeBaseCommit returns the commit a worktree started from ("" for rows
// made before it was recorded).
func (s *Store) WorktreeBaseCommit(ctx context.Context, id core.WorktreeID) (string, error) {
	c, err := s.db.reader().GetWorktreeBaseCommit(ctx, string(id))
	if err := found("get worktree base commit "+string(id), err); err != nil {
		return "", err
	}
	return c, nil
}
