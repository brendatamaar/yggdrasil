package builds

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// NewBuild is the expected version that makes SaveBuild insert (None to
// Queued).
const NewBuild = db.NewBuild

// BuildTx is the unit of work inside BuildStore.Commit. State and its audit
// entries are committed together or not at all.
//
// Errors: db.ErrNotFound (missing), db.ErrConflict (stale version, taken ID or
// taken preset name).
type BuildTx interface {
	// LoadBuild returns the state and version of a build.
	LoadBuild(ctx context.Context, id core.BuildID) (corebuilds.State, int64, error)
	// SaveBuild inserts (expected == NewBuild) or updates a build, rewriting
	// its steps and artifacts, and returns the new version.
	SaveBuild(ctx context.Context, s corebuilds.State, expected int64) (int64, error)
	AppendAudit(ctx context.Context, e core.AuditEntry) error
	InsertPreset(ctx context.Context, p corebuilds.Preset, now time.Time) error
	UpdatePreset(ctx context.Context, p corebuilds.Preset, expected int64) (int64, error)
}

// BuildStore is what the builds service needs from storage.
type BuildStore interface {
	Commit(ctx context.Context, fn func(BuildTx) error) error
	// ListQueued: queued builds, oldest first.
	ListQueued(ctx context.Context) ([]corebuilds.QueuedRef, error)
	// CountRunning: builds that are Running or Cancelling.
	CountRunning(ctx context.Context) (int, error)
	// ListActive: Running and Cancelling builds, oldest first.
	ListActive(ctx context.Context) ([]corebuilds.State, error)
	Get(ctx context.Context, id core.BuildID) (corebuilds.State, int64, error)
	// List: newest first.
	List(ctx context.Context, f db.BuildFilter) ([]corebuilds.State, error)
	GetPreset(ctx context.Context, id corebuilds.PresetID) (corebuilds.Preset, int64, error)
	ListPresets(ctx context.Context, project core.ProjectID) ([]corebuilds.Preset, error)
}

// SQLiteStore adapts *db.Store to BuildStore.
type SQLiteStore struct{ s *db.Store }

// NewSQLiteStore returns a BuildStore on s.
func NewSQLiteStore(s *db.Store) *SQLiteStore { return &SQLiteStore{s: s} }

// Commit runs fn in one write transaction.
func (a *SQLiteStore) Commit(ctx context.Context, fn func(BuildTx) error) error {
	return a.s.Commit(ctx, func(tx *db.Tx) error { return fn(tx) })
}

func (a *SQLiteStore) ListQueued(ctx context.Context) ([]corebuilds.QueuedRef, error) {
	return a.s.ListQueued(ctx)
}
func (a *SQLiteStore) CountRunning(ctx context.Context) (int, error) { return a.s.CountRunning(ctx) }
func (a *SQLiteStore) ListActive(ctx context.Context) ([]corebuilds.State, error) {
	return a.s.ListActive(ctx)
}
func (a *SQLiteStore) Get(ctx context.Context, id core.BuildID) (corebuilds.State, int64, error) {
	return a.s.GetBuildState(ctx, id)
}
func (a *SQLiteStore) List(ctx context.Context, f db.BuildFilter) ([]corebuilds.State, error) {
	return a.s.ListBuildStates(ctx, f)
}
func (a *SQLiteStore) GetPreset(ctx context.Context, id corebuilds.PresetID) (corebuilds.Preset, int64, error) {
	return a.s.GetPreset(ctx, id)
}
func (a *SQLiteStore) ListPresets(ctx context.Context, p core.ProjectID) ([]corebuilds.Preset, error) {
	return a.s.ListPresets(ctx, p)
}

var _ BuildStore = (*SQLiteStore)(nil)
