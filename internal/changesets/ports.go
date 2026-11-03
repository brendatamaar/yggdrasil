// Package changesets is the shell of change sets and review notes (RV-01,
// RV-03, RV-04; docs/design/changesets.md). The decisions are pure and live in
// internal/core/changesets (StepSet) and internal/core/review (StepNote,
// StepBatch). This package parses outside data into core types at the
// boundary, loads the state, calls the step, writes the new state and its
// audit entries in one transaction, and only then runs the commands: confirm
// one proposal through the proposals service, send a message to the task's
// agent, tell the UI.
//
// It starts no process. Every goroutine is owned by the context of Run.
package changesets

import (
	"context"
	"errors"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Refused is a plain "no" from the proposals service to a Confirm: the hash is
// stale, the proposal is not pending, it no longer exists or its repo is
// untracked. No state changed. Anything else a ProposalsAPI returns is a
// transient failure and is retried by the next pass.
type Refused struct {
	// Message is plain language for the user.
	Message string
	// AlreadyDecided: the proposal was no longer pending. When the shell
	// re-sends a confirmation after a crash this is not a problem.
	AlreadyDecided bool
}

func (r *Refused) Error() string       { return "proposal refused: " + r.Message }
func (r *Refused) UserMessage() string { return r.Message }

// UserError is a mistake the person can fix; Message says how.
type UserError struct {
	Message string
	// Kind says whether the request is wrong (KindInvalid, the default) or is
	// right but the change set state does not allow it now (KindConflict).
	Kind UserErrorKind
}

// UserErrorKind classifies a UserError for the API (400 vs 409).
type UserErrorKind int

const (
	// KindInvalid: the request is wrong; fix it and send again.
	KindInvalid UserErrorKind = iota
	// KindConflict: the request is fine but the state does not allow it now.
	KindConflict
)

func (e *UserError) Error() string       { return e.Message }
func (e *UserError) UserMessage() string { return e.Message }

// ProposalsAPI is what the shell needs from the proposals service: confirm
// one proposal with the hash the user saw. It is the only way a run moves a
// proposal (the run never executes anything itself). *proposals.Service is
// adapted in cmd/ygg/changesets.go. Contract: ProposalsAPIContract.
type ProposalsAPI interface {
	// Confirm returns nil when the proposal is now confirmed (or further),
	// *Refused for a "no", any other error for a transient failure.
	Confirm(ctx context.Context, id core.ProposalID, hash, by string) error
}

// ErrAgentBusy and ErrQueueFull are what a Messenger wraps when the task is
// working or the queue has no room. Only these get the "agent is busy" text;
// any other failure is reported as unavailable.
var (
	ErrAgentBusy = errors.New("the agent is busy")
	ErrQueueFull = errors.New("the message queue is full")
)

// Messenger sends text to the task's agent as a new message in the same mode
// (RV-01). It returns the new turn. It is not idempotent. A busy task, a full
// queue or any other refusal is an error; if it has a UserMessage method the
// text is shown, else a generic sentence is.
type Messenger interface {
	SendMessage(ctx context.Context, task core.TaskID, text string) (turn string, err error)
}

// DepSource reads the dependency edges between repos from their manifests
// (files only; Maven at v1). A repo it cannot read has no edges.
type DepSource interface {
	Edges(ctx context.Context, repos []core.RepoID) ([]cs.Edge, error)
}

// DiffSource returns the per-file diff hashes of a task's worktree in a repo
// (git BaseFile.Hash). An error means the diff could not be read; the shell
// then does not judge the notes of that repo.
type DiffSource interface {
	FileHashes(ctx context.Context, task core.TaskID, repo core.RepoID) (map[review.RelPath]string, error)
}

// Publisher tells the UI that something changed (hub topic "changesets"). It
// carries no code, path or note text.
type Publisher interface {
	PublishChangeSet(id core.ChangeSetID, task core.TaskID, status core.ChangeSetStatus)
	PublishNotes(task core.TaskID)
}

// Tx is the unit of work of Store.Commit: the state rows and their audit
// entries are written together or not at all (invariant 8).
//
// Errors: db.ErrNotFound (missing), db.ErrConflict (stale version, taken ID).
type Tx interface {
	LoadSet(ctx context.Context, id core.ChangeSetID) (db.ChangeSetRow, error)
	// SaveSet stores status, merge order and run result if the row is still
	// at expected, and returns the new version.
	SaveSet(ctx context.Context, row db.ChangeSetRow, expected int64) (int64, error)

	LoadNote(ctx context.Context, id string) (db.NoteRow, error)
	CountOpenNotes(ctx context.Context, task core.TaskID) (int, error)
	InsertNote(ctx context.Context, n db.NoteRow) error
	UpdateNote(ctx context.Context, n db.NoteRow, expected int64) (int64, error)
	// DeleteNote removes an open or outdated note; any other is db.ErrNotFound.
	DeleteNote(ctx context.Context, id string) error

	// WriteAudit prepares the entry (checks it, redacts it, gives it an ID and
	// the time) and writes it. Actor, Action, Subject and Detail are used.
	WriteAudit(ctx context.Context, e core.AuditEntry, now time.Time) error
}

// Store is what the service needs from storage. *SQLiteStore implements it
// over internal/db; changesetstest has an in-memory fake. Both run
// ChangeSetStoreContract.
type Store interface {
	Commit(ctx context.Context, fn func(Tx) error) error

	Set(ctx context.Context, id core.ChangeSetID) (db.ChangeSetRow, error)
	SetOfProposal(ctx context.Context, id core.ProposalID) (db.ChangeSetRow, error)
	// SetOfTask returns the task's change set ID, or "" when it has none.
	SetOfTask(ctx context.Context, task core.TaskID) (core.ChangeSetID, error)
	// OpenSets lists the sets whose status is open.
	OpenSets(ctx context.Context) ([]db.ChangeSetRow, error)
	// Members lists only the proposals of that set, oldest first.
	Members(ctx context.Context, id core.ChangeSetID) ([]db.ChangeSetMemberRow, error)
	TaskRepos(ctx context.Context, task core.TaskID) ([]core.RepoID, error)

	Note(ctx context.Context, id string) (db.NoteRow, error)
	TaskNotes(ctx context.Context, task core.TaskID) ([]db.NoteRow, error)
	// NotesInState lists notes with a stored state ("sending", ...).
	NotesInState(ctx context.Context, state string) ([]db.NoteRow, error)
	TasksWithNotesInState(ctx context.Context, state string) ([]core.TaskID, error)
}
