package devservers

import (
	"context"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Tx is the unit of work of Store.Commit: the server row and its audit entries
// are written together or not at all (invariant 8).
//
// Errors: db.ErrNotFound (missing server, repo or worktree), db.ErrConflict
// (stale version, taken ID).
type Tx interface {
	Load(ctx context.Context, id string) (db.DevServerRow, error)
	// Save inserts (expected == db.NewDevServer) or updates the row if it is
	// still at expected, and returns the new version. now is the write time.
	// The row must be consistent (dsc.ServerFromRow accepts it).
	Save(ctx context.Context, id string, row dsc.Row, expected int64, now time.Time) (int64, error)
	// Audit prepares e (audit.Prepare redacts it; id and at are the caller's) and
	// writes it in the same transaction.
	Audit(ctx context.Context, e core.AuditEntry, id core.AuditID, at time.Time) error
}

// Store is what the service needs from storage. *SQLiteStore implements it
// over internal/db; devserverstest has an in-memory fake. Both run
// DevServerStoreContract.
type Store interface {
	Commit(ctx context.Context, fn func(Tx) error) error
	// Get returns db.ErrNotFound for an unknown server.
	Get(ctx context.Context, id string) (db.DevServerRow, error)
	// ListLive returns exactly the starting, running, stopping and orphaned
	// servers, by ID.
	ListLive(ctx context.Context) ([]db.DevServerRow, error)
	ListByRepo(ctx context.Context, repo core.RepoID) ([]db.DevServerRow, error)
}

// SQLiteStore adapts *db.Store to Store.
type SQLiteStore struct {
	S        *db.Store
	Redactor safety.Redactor
}

// NewSQLiteStore returns a Store on s whose audit entries are redacted with r
// (invariant 4).
func NewSQLiteStore(s *db.Store, r safety.Redactor) *SQLiteStore {
	return &SQLiteStore{S: s, Redactor: r}
}

type sqliteTx struct {
	tx *db.Tx
	r  safety.Redactor
}

func (t sqliteTx) Load(ctx context.Context, id string) (db.DevServerRow, error) {
	return t.tx.LoadDevServer(ctx, id)
}

func (t sqliteTx) Save(ctx context.Context, id string, row dsc.Row, expected int64, now time.Time) (int64, error) {
	return t.tx.SaveDevServer(ctx, id, row, expected, now)
}

func (t sqliteTx) Audit(ctx context.Context, e core.AuditEntry, id core.AuditID, at time.Time) error {
	p, err := audit.Prepare(e, t.r, id, at)
	if err != nil {
		return fmt.Errorf("prepare audit entry: %w", err)
	}
	return t.tx.AppendAudit(ctx, p)
}

// Commit runs fn in one write transaction.
func (a *SQLiteStore) Commit(ctx context.Context, fn func(Tx) error) error {
	return a.S.Commit(ctx, func(tx *db.Tx) error { return fn(sqliteTx{tx: tx, r: a.Redactor}) })
}

// Get implements Store.
func (a *SQLiteStore) Get(ctx context.Context, id string) (db.DevServerRow, error) {
	return a.S.GetDevServer(ctx, id)
}

// ListLive implements Store.
func (a *SQLiteStore) ListLive(ctx context.Context) ([]db.DevServerRow, error) {
	return a.S.ListLiveDevServers(ctx)
}

// ListByRepo implements Store.
func (a *SQLiteStore) ListByRepo(ctx context.Context, repo core.RepoID) ([]db.DevServerRow, error) {
	return a.S.ListDevServersByRepo(ctx, repo)
}
