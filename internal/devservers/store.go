package devservers

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/db"
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
	AppendAudit(ctx context.Context, e core.AuditEntry) error
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
type SQLiteStore struct{ S *db.Store }

// NewSQLiteStore returns a Store on s.
func NewSQLiteStore(s *db.Store) *SQLiteStore { return &SQLiteStore{S: s} }

type sqliteTx struct{ tx *db.Tx }

func (t sqliteTx) Load(ctx context.Context, id string) (db.DevServerRow, error) {
	return t.tx.LoadDevServer(ctx, id)
}

func (t sqliteTx) Save(ctx context.Context, id string, row dsc.Row, expected int64, now time.Time) (int64, error) {
	return t.tx.SaveDevServer(ctx, id, row, expected, now)
}

func (t sqliteTx) AppendAudit(ctx context.Context, e core.AuditEntry) error {
	return t.tx.AppendAudit(ctx, e)
}

// Commit runs fn in one write transaction.
func (a *SQLiteStore) Commit(ctx context.Context, fn func(Tx) error) error {
	return a.S.Commit(ctx, func(tx *db.Tx) error { return fn(sqliteTx{tx}) })
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
