package verify

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// The entries this adapter forwards are prepared (redacted, given an ID) by
// the service with audit.Prepare before they reach AppendAudit; the reference
// keeps the audit rule of internal/archtest honest about it.
var _ = audit.Prepare

// Tx is the unit of work of Store.Commit: the verification row and its audit
// entries are written together or not at all (invariant 8).
//
// Errors: db.ErrNotFound (missing), db.ErrConflict (stale version, taken
// ID or (task, repo)).
type Tx interface {
	LoadByTaskRepo(ctx context.Context, task core.TaskID, repo core.RepoID) (db.VerificationRow, error)
	// Save inserts (expected == db.NewVerification) or updates the row if it
	// is still at expected, and returns the new version. now is the write time.
	Save(ctx context.Context, row db.VerificationRow, expected int64, now time.Time) (int64, error)
	AppendAudit(ctx context.Context, e core.AuditEntry) error
}

// Store is what the verify service needs from storage. *SQLiteStore
// implements it over internal/db; verifytest has an in-memory fake. Both run
// VerifyStoreContract.
type Store interface {
	Commit(ctx context.Context, fn func(Tx) error) error
	Get(ctx context.Context, id string) (db.VerificationRow, error)
	GetByTaskRepo(ctx context.Context, task core.TaskID, repo core.RepoID) (db.VerificationRow, error)
	ListByTask(ctx context.Context, task core.TaskID) ([]db.VerificationRow, error)
	// ListLive: bootstrapping and verifying rows.
	ListLive(ctx context.Context) ([]db.VerificationRow, error)
	ListInterrupted(ctx context.Context) ([]db.VerificationRow, error)
	// ReviewTurns: the last turn of every task idle in review whose turn ended
	// ready for review.
	ReviewTurns(ctx context.Context) ([]db.ReviewTurn, error)
	// Worktrees: the ACTIVE worktrees of a task. Merged, removing and removed
	// ones are not returned.
	Worktrees(ctx context.Context, task core.TaskID) ([]core.Worktree, error)
	// BaseCommit is the commit the worktree started from.
	BaseCommit(ctx context.Context, wt core.WorktreeID) (string, error)
	// BuildSettings returns the repo's settings (defaults when none are
	// stored).
	BuildSettings(ctx context.Context, repo core.RepoID) (db.BuildSettingsRecord, error)
	// RepoTrusted reports SF-09 trust.
	RepoTrusted(ctx context.Context, repo core.RepoID) (bool, error)
	// AutoVerify is the project switch "run verification automatically"
	// (setting verify.auto); on when it was never set.
	AutoVerify(ctx context.Context) (bool, error)
}

// SettingAutoVerify is the key of the "run verification automatically"
// setting. "false", "off", "0" and "no" switch it off; anything else, or no
// value, is on.
const SettingAutoVerify = "verify.auto"

// SQLiteStore adapts *db.Store to Store.
type SQLiteStore struct{ S *db.Store }

// NewSQLiteStore returns a Store on s.
func NewSQLiteStore(s *db.Store) *SQLiteStore { return &SQLiteStore{S: s} }

type sqliteTx struct{ tx *db.Tx }

func (t sqliteTx) LoadByTaskRepo(ctx context.Context, task core.TaskID, repo core.RepoID) (db.VerificationRow, error) {
	return t.tx.LoadVerificationByTaskRepo(ctx, task, repo)
}

func (t sqliteTx) Save(ctx context.Context, row db.VerificationRow, expected int64, now time.Time) (int64, error) {
	return t.tx.SaveVerification(ctx, row, expected, now)
}

func (t sqliteTx) AppendAudit(ctx context.Context, e core.AuditEntry) error {
	return t.tx.AppendAudit(ctx, e)
}

// Commit runs fn in one write transaction.
func (a *SQLiteStore) Commit(ctx context.Context, fn func(Tx) error) error {
	return a.S.Commit(ctx, func(tx *db.Tx) error { return fn(sqliteTx{tx}) })
}

func (a *SQLiteStore) Get(ctx context.Context, id string) (db.VerificationRow, error) {
	return a.S.GetVerification(ctx, id)
}

func (a *SQLiteStore) GetByTaskRepo(ctx context.Context, task core.TaskID, repo core.RepoID) (db.VerificationRow, error) {
	return a.S.GetVerificationByTaskRepo(ctx, task, repo)
}

func (a *SQLiteStore) ListByTask(ctx context.Context, task core.TaskID) ([]db.VerificationRow, error) {
	return a.S.ListVerificationsByTask(ctx, task)
}

func (a *SQLiteStore) ListLive(ctx context.Context) ([]db.VerificationRow, error) {
	return a.S.ListLiveVerifications(ctx)
}

func (a *SQLiteStore) ListInterrupted(ctx context.Context) ([]db.VerificationRow, error) {
	return a.S.ListInterruptedVerifications(ctx)
}

func (a *SQLiteStore) ReviewTurns(ctx context.Context) ([]db.ReviewTurn, error) {
	return a.S.ListReviewTurns(ctx)
}

func (a *SQLiteStore) Worktrees(ctx context.Context, task core.TaskID) ([]core.Worktree, error) {
	all, err := a.S.ListWorktrees(ctx, task)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, w := range all {
		if w.Status == core.WorktreeActive {
			out = append(out, w)
		}
	}
	return out, nil
}

func (a *SQLiteStore) BaseCommit(ctx context.Context, wt core.WorktreeID) (string, error) {
	return a.S.GetWorktreeBaseCommit(ctx, wt)
}

func (a *SQLiteStore) BuildSettings(ctx context.Context, repo core.RepoID) (db.BuildSettingsRecord, error) {
	return a.S.GetBuildSettings(ctx, repo)
}

func (a *SQLiteStore) RepoTrusted(ctx context.Context, repo core.RepoID) (bool, error) {
	trusted, _, err := a.S.GetRepoTrust(ctx, repo)
	return trusted, err
}

func (a *SQLiteStore) AutoVerify(ctx context.Context) (bool, error) {
	s, err := a.S.GetSetting(ctx, SettingAutoVerify)
	if err != nil {
		if isNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return ParseAutoVerify(s.Value), nil
}

// ParseAutoVerify reads the value of the verify.auto setting.
func ParseAutoVerify(v string) bool {
	switch v {
	case "false", "off", "0", "no":
		return false
	}
	return true
}
