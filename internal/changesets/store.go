package changesets

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// SQLiteStore adapts *db.Store to Store. Its transaction prepares every audit
// entry itself (audit.Prepare: checked, redacted, given an ID and the time),
// so the single place that calls AppendAudit is also the place that prepares.
type SQLiteStore struct {
	S        *db.Store
	Redactor safety.Redactor
	// IDs makes IDs with a prefix; default db.NewID.
	IDs func(prefix string) string
}

// NewSQLiteStore returns a Store on s. A nil redactor redacts with the
// default rules.
func NewSQLiteStore(s *db.Store, r safety.Redactor) *SQLiteStore {
	if r == nil {
		r = safety.NewRedactor()
	}
	return &SQLiteStore{S: s, Redactor: r, IDs: db.NewID}
}

type sqliteTx struct {
	tx  *db.Tx
	red safety.Redactor
	ids func(string) string
}

func (t sqliteTx) LoadSet(ctx context.Context, id core.ChangeSetID) (db.ChangeSetRow, error) {
	return t.tx.LoadChangeSetRow(ctx, id)
}

func (t sqliteTx) SaveSet(ctx context.Context, row db.ChangeSetRow, expected int64) (int64, error) {
	return t.tx.SaveChangeSetRun(ctx, row, expected)
}

func (t sqliteTx) LoadNote(ctx context.Context, id string) (db.NoteRow, error) {
	return t.tx.LoadNoteRow(ctx, id)
}

func (t sqliteTx) CountOpenNotes(ctx context.Context, task core.TaskID) (int, error) {
	return t.tx.CountOpenNotes(ctx, task)
}

func (t sqliteTx) InsertNote(ctx context.Context, n db.NoteRow) error {
	return t.tx.InsertNoteRow(ctx, n)
}

func (t sqliteTx) UpdateNote(ctx context.Context, n db.NoteRow, expected int64) (int64, error) {
	return t.tx.UpdateNoteRow(ctx, n, expected)
}

func (t sqliteTx) DeleteNote(ctx context.Context, id string) error {
	return t.tx.DeleteNoteRow(ctx, id)
}

func (t sqliteTx) AppendAudit(ctx context.Context, e core.AuditEntry, now time.Time) error {
	p, err := audit.Prepare(e, t.red, core.AuditID(t.ids("aud")), now)
	if err != nil {
		return err
	}
	return t.tx.AppendAudit(ctx, p)
}

// Commit runs fn in one write transaction.
func (a *SQLiteStore) Commit(ctx context.Context, fn func(Tx) error) error {
	ids := a.IDs
	if ids == nil {
		ids = db.NewID
	}
	return a.S.Commit(ctx, func(tx *db.Tx) error { return fn(sqliteTx{tx: tx, red: a.Redactor, ids: ids}) })
}

func (a *SQLiteStore) Set(ctx context.Context, id core.ChangeSetID) (db.ChangeSetRow, error) {
	return a.S.GetChangeSetRow(ctx, id)
}

func (a *SQLiteStore) SetOfProposal(ctx context.Context, id core.ProposalID) (db.ChangeSetRow, error) {
	return a.S.GetChangeSetRowByProposal(ctx, id)
}

func (a *SQLiteStore) SetOfTask(ctx context.Context, task core.TaskID) (core.ChangeSetID, error) {
	return a.S.GetTaskChangeSetID(ctx, task)
}

func (a *SQLiteStore) OpenSets(ctx context.Context) ([]db.ChangeSetRow, error) {
	return a.S.ListChangeSetRowsByStatus(ctx, core.ChangeSetOpen)
}

func (a *SQLiteStore) Members(ctx context.Context, id core.ChangeSetID) ([]db.ChangeSetMemberRow, error) {
	return a.S.ListChangeSetMembers(ctx, id)
}

func (a *SQLiteStore) TaskRepos(ctx context.Context, task core.TaskID) ([]core.RepoID, error) {
	return a.S.ListTaskRepoIDs(ctx, task)
}

func (a *SQLiteStore) Note(ctx context.Context, id string) (db.NoteRow, error) {
	return a.S.GetNoteRow(ctx, id)
}

func (a *SQLiteStore) TaskNotes(ctx context.Context, task core.TaskID) ([]db.NoteRow, error) {
	return a.S.ListNoteRowsByTask(ctx, task)
}

func (a *SQLiteStore) NotesInState(ctx context.Context, state string) ([]db.NoteRow, error) {
	return a.S.ListNoteRowsByState(ctx, state)
}

func (a *SQLiteStore) TasksWithNotesInState(ctx context.Context, state string) ([]core.TaskID, error) {
	return a.S.ListTasksWithNotesInState(ctx, state)
}
