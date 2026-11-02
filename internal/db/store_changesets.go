package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// ChangeSetRow is a change_sets row as plain values. This package does not
// interpret it: internal/changesets parses it into core/changesets types at
// the boundary (StateFromRows) and rejects inconsistent rows.
type ChangeSetRow struct {
	ID     core.ChangeSetID
	Task   core.TaskID
	Status core.ChangeSetStatus
	// MergeOrder is the stored merge order (may be empty or partial for a set
	// that never had its order edited).
	MergeOrder []core.RepoID
	// RunResult is the JSON of the last or current confirm run (never empty).
	RunResult json.RawMessage
	Version   int64
	CreatedAt time.Time
}

// ChangeSetMemberRow is one proposal of a change set, as plain values.
type ChangeSetMemberRow struct {
	ID     core.ProposalID
	Repo   core.RepoID
	Kind   core.ProposalKind
	Status core.ProposalStatus
	Hash   string
	// MRURL is the merge request URL of an executed proposal's result, "" for
	// anything else.
	MRURL string
}

// NoteRow is a review_notes row as plain values; internal/changesets parses it
// into a core/review.Note.
type NoteRow struct {
	ID        string
	Task      core.TaskID
	Repo      core.RepoID
	Path      string
	Line      int
	Side      string
	Text      string
	DiffHash  string
	State     string
	Batch     string
	Turn      string
	StateAt   *time.Time
	Version   int64
	CreatedAt time.Time
}

func changeSetRowFrom(r gen.ChangeSet) (ChangeSetRow, error) {
	at, err := parseTime(r.CreatedAt)
	if err != nil {
		return ChangeSetRow{}, fmt.Errorf("change set %s: %w", r.ID, err)
	}
	order := []core.RepoID{}
	if err := json.Unmarshal([]byte(r.MergeOrder), &order); err != nil {
		return ChangeSetRow{}, fmt.Errorf("change set %s: bad merge order in database: %w", r.ID, err)
	}
	return ChangeSetRow{
		ID: core.ChangeSetID(r.ID), Task: core.TaskID(r.TaskID), Status: core.ChangeSetStatus(r.Status),
		MergeOrder: order, RunResult: json.RawMessage(r.RunResult), Version: r.Version, CreatedAt: at,
	}, nil
}

func memberRowsFrom(rows []gen.Proposal) ([]ChangeSetMemberRow, error) {
	recs, err := recordsFrom(rows)
	if err != nil {
		return nil, err
	}
	out := make([]ChangeSetMemberRow, 0, len(recs))
	for _, rec := range recs {
		p := rec.Proposal
		hash := p.ActionHash
		if hash == "" {
			hash = proposals.Hash(p.Action) // rows from before the hash column
		}
		m := ChangeSetMemberRow{ID: p.ID, Repo: p.Repo, Kind: p.Action.Kind(), Status: p.State.Status(), Hash: hash}
		if ex, ok := p.State.(proposals.Executed); ok {
			m.MRURL = ex.Result.MRURL
		}
		out = append(out, m)
	}
	return out, nil
}

// GetChangeSetRow returns one change set with its run result and version, or
// ErrNotFound.
func (s *Store) GetChangeSetRow(ctx context.Context, id core.ChangeSetID) (ChangeSetRow, error) {
	r, err := s.db.reader().GetChangeSet(ctx, string(id))
	if err := found("get change set "+string(id), err); err != nil {
		return ChangeSetRow{}, err
	}
	return changeSetRowFrom(r)
}

// GetChangeSetRowByProposal returns the change set a proposal belongs to, or
// ErrNotFound.
func (s *Store) GetChangeSetRowByProposal(ctx context.Context, id core.ProposalID) (ChangeSetRow, error) {
	r, err := s.db.reader().GetChangeSetByProposal(ctx, string(id))
	if err := found("get change set of proposal "+string(id), err); err != nil {
		return ChangeSetRow{}, err
	}
	return changeSetRowFrom(r)
}

// ListChangeSetRowsByStatus returns the change sets with a status, oldest first.
func (s *Store) ListChangeSetRowsByStatus(ctx context.Context, status core.ChangeSetStatus) ([]ChangeSetRow, error) {
	rows, err := s.db.reader().ListChangeSetsByStatus(ctx, string(status))
	if err != nil {
		return nil, fmt.Errorf("list change sets: %w", err)
	}
	out := make([]ChangeSetRow, 0, len(rows))
	for _, r := range rows {
		c, err := changeSetRowFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ListChangeSetMembers returns the proposals of a change set as plain rows,
// oldest first. A proposal whose stored action does not parse is an error.
func (s *Store) ListChangeSetMembers(ctx context.Context, id core.ChangeSetID) ([]ChangeSetMemberRow, error) {
	rows, err := s.db.reader().ListProposalsByChangeSet(ctx, nullStr(&id))
	if err != nil {
		return nil, fmt.Errorf("list proposals of change set %s: %w", id, err)
	}
	return memberRowsFrom(rows)
}

// ListTaskRepoIDs returns the repos in a task's scope, sorted by ID.
func (s *Store) ListTaskRepoIDs(ctx context.Context, task core.TaskID) ([]core.RepoID, error) {
	ids, err := s.db.reader().ListTaskRepos(ctx, string(task))
	if err != nil {
		return nil, fmt.Errorf("list repos of task %s: %w", task, err)
	}
	out := make([]core.RepoID, len(ids))
	for i, id := range ids {
		out[i] = core.RepoID(id)
	}
	return out, nil
}

// LoadChangeSetRow is GetChangeSetRow inside a write transaction.
func (tx *Tx) LoadChangeSetRow(ctx context.Context, id core.ChangeSetID) (ChangeSetRow, error) {
	r, err := tx.q.GetChangeSet(ctx, string(id))
	if err := found("load change set "+string(id), err); err != nil {
		return ChangeSetRow{}, err
	}
	return changeSetRowFrom(r)
}

// SaveChangeSetRun stores the status, merge order and run result of a change
// set if it is still at expected, and returns the new version. A stale version
// returns ErrConflict, a missing set ErrNotFound. Call it with AppendAudit in
// the same Commit (invariant 8).
func (tx *Tx) SaveChangeSetRun(ctx context.Context, row ChangeSetRow, expected int64) (int64, error) {
	if err := row.Status.Validate(); err != nil {
		return 0, err
	}
	run := jsonText(row.RunResult, `{"kind":"idle"}`)
	if !json.Valid([]byte(run)) {
		return 0, fmt.Errorf("save change set %s: the run result is not valid JSON", row.ID)
	}
	order := row.MergeOrder
	if order == nil {
		order = []core.RepoID{}
	}
	ob, err := json.Marshal(order)
	if err != nil {
		return 0, fmt.Errorf("save change set %s: %w", row.ID, err)
	}
	what := "save change set " + string(row.ID)
	n, err := tx.q.UpdateChangeSetRun(ctx, gen.UpdateChangeSetRunParams{
		Status: string(row.Status), MergeOrder: string(ob), RunResult: run, ID: string(row.ID), Version: expected,
	})
	return bumped(what, n, err, expected, func() error {
		_, err := tx.q.GetChangeSet(ctx, string(row.ID))
		return err
	})
}

func noteRowFrom(r gen.ReviewNote) (NoteRow, error) {
	at, err := parseTime(r.CreatedAt)
	if err != nil {
		return NoteRow{}, fmt.Errorf("note %s: %w", r.ID, err)
	}
	sat, err := parseNullTime(r.StateAt)
	if err != nil {
		return NoteRow{}, fmt.Errorf("note %s: %w", r.ID, err)
	}
	return NoteRow{
		ID: r.ID, Task: core.TaskID(r.TaskID), Repo: core.RepoID(r.RepoID), Path: r.Path, Line: int(r.Line), Side: r.Side,
		Text: r.Text, DiffHash: r.DiffHash, State: r.State, Batch: r.BatchID, Turn: r.TurnID, StateAt: sat,
		Version: r.Version, CreatedAt: at,
	}, nil
}

func noteRowsFrom(rows []gen.ReviewNote) ([]NoteRow, error) {
	out := make([]NoteRow, 0, len(rows))
	for _, r := range rows {
		n, err := noteRowFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// GetNoteRow returns one review note, or ErrNotFound.
func (s *Store) GetNoteRow(ctx context.Context, id string) (NoteRow, error) {
	r, err := s.db.reader().GetReviewNote(ctx, id)
	if err := found("get note "+id, err); err != nil {
		return NoteRow{}, err
	}
	return noteRowFrom(r)
}

// ListNoteRowsByTask returns the notes of a task, oldest first.
func (s *Store) ListNoteRowsByTask(ctx context.Context, task core.TaskID) ([]NoteRow, error) {
	rows, err := s.db.reader().ListReviewNotesByTask(ctx, string(task))
	if err != nil {
		return nil, fmt.Errorf("list notes of %s: %w", task, err)
	}
	return noteRowsFrom(rows)
}

// ListNoteRowsByState returns the notes in a state ("open", "sending", "sent",
// "outdated"), oldest first.
func (s *Store) ListNoteRowsByState(ctx context.Context, state string) ([]NoteRow, error) {
	rows, err := s.db.reader().ListReviewNotesByState(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	return noteRowsFrom(rows)
}

// CountOpenNotes counts the open and outdated notes of a task.
func (s *Store) CountOpenNotes(ctx context.Context, task core.TaskID) (int, error) {
	n, err := s.db.reader().CountOpenReviewNotesByTask(ctx, string(task))
	if err != nil {
		return 0, fmt.Errorf("count notes of %s: %w", task, err)
	}
	return int(n), nil
}

// ListTasksWithNotesInState returns the tasks that have a note in the state.
func (s *Store) ListTasksWithNotesInState(ctx context.Context, state string) ([]core.TaskID, error) {
	ids, err := s.db.reader().ListTasksWithReviewNotesInState(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("list tasks with %s notes: %w", state, err)
	}
	out := make([]core.TaskID, len(ids))
	for i, id := range ids {
		out[i] = core.TaskID(id)
	}
	return out, nil
}

// LoadNoteRow is GetNoteRow inside a write transaction.
func (tx *Tx) LoadNoteRow(ctx context.Context, id string) (NoteRow, error) {
	r, err := tx.q.GetReviewNote(ctx, id)
	if err := found("load note "+id, err); err != nil {
		return NoteRow{}, err
	}
	return noteRowFrom(r)
}

// CountOpenNotes is Store.CountOpenNotes inside a write transaction.
func (tx *Tx) CountOpenNotes(ctx context.Context, task core.TaskID) (int, error) {
	n, err := tx.q.CountOpenReviewNotesByTask(ctx, string(task))
	if err != nil {
		return 0, fmt.Errorf("count notes of %s: %w", task, err)
	}
	return int(n), nil
}

// InsertNoteRow stores a new note (ErrConflict if the ID exists, ErrNotFound
// if the task or repo is gone). The caller writes the audit entry in the same
// Commit. Notes are text for the agent, not secrets, but they are redacted on
// the way in like every stored free text.
func (tx *Tx) InsertNoteRow(ctx context.Context, n NoteRow) error {
	if n.ID == "" {
		return fmt.Errorf("insert note: the note has no ID")
	}
	if err := needTime("note", n.CreatedAt); err != nil {
		return err
	}
	err := tx.q.InsertReviewNote(ctx, gen.InsertReviewNoteParams{
		ID: n.ID, TaskID: string(n.Task), RepoID: string(n.Repo), Path: n.Path, Line: int64(n.Line), Side: n.Side,
		Text: tx.redact(n.Text), DiffHash: n.DiffHash, State: n.State, BatchID: n.Batch, TurnID: n.Turn,
		StateAt: fmtNullTime(n.StateAt), CreatedAt: fmtTime(n.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("insert note %s: %w", n.ID, err)
	}
	return nil
}

// UpdateNoteRow stores the text, state, batch, turn and state time of a note
// if it is still at expected, and returns the new version (ErrConflict if it
// moved, ErrNotFound if it is gone).
func (tx *Tx) UpdateNoteRow(ctx context.Context, n NoteRow, expected int64) (int64, error) {
	what := "update note " + n.ID
	cnt, err := tx.q.UpdateReviewNote(ctx, gen.UpdateReviewNoteParams{
		Text: tx.redact(n.Text), State: n.State, BatchID: n.Batch, TurnID: n.Turn, StateAt: fmtNullTime(n.StateAt),
		ID: n.ID, Version: expected,
	})
	return bumped(what, cnt, err, expected, func() error {
		_, err := tx.q.GetReviewNote(ctx, n.ID)
		return err
	})
}

// DeleteNoteRow removes an open or outdated note. A sent or sending note is
// history and is not removed (ErrNotFound is returned for it, too).
func (tx *Tx) DeleteNoteRow(ctx context.Context, id string) error {
	n, err := tx.q.DeleteReviewNote(ctx, id)
	return updated("delete note "+id, n, err)
}
