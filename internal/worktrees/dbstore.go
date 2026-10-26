package worktrees

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// DBStore is the Store backed by the SQLite store.
type DBStore struct{ S *db.Store }

// List implements Store.
func (d DBStore) List(ctx context.Context, task core.TaskID) ([]core.Worktree, error) {
	return d.S.ListWorktrees(ctx, task)
}

// Record implements Store: row, base commit and audit entry in one commit.
func (d DBStore) Record(ctx context.Context, w core.Worktree, baseCommit string, audit core.AuditEntry) error {
	return d.S.Commit(ctx, func(tx *db.Tx) error {
		_, err := tx.RecordWorktree(ctx, w, baseCommit, audit)
		return err
	})
}
