package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// ProposalRecord is a stored proposal with the row version that a later Save
// must present.
type ProposalRecord struct {
	Proposal proposals.Proposal
	Version  int64
}

// resultJSON is the stored form of proposals.Result (strict: unknown keys are
// refused on load).
type resultJSON struct {
	CommitID   string   `json:"commit_id,omitempty"`
	MRURL      string   `json:"mr_url,omitempty"`
	MovedPaths []string `json:"moved_paths,omitempty"`
	BuildID    string   `json:"build_id,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func encodeResult(r proposals.Result, redact func(string) string) (string, error) {
	j := resultJSON{CommitID: r.CommitID, MRURL: redact(r.MRURL), BuildID: string(r.BuildID), Note: redact(r.Note)}
	for _, p := range r.MovedPaths {
		j.MovedPaths = append(j.MovedPaths, string(p))
	}
	b, err := json.Marshal(j)
	if err != nil {
		return "", fmt.Errorf("encode proposal result: %w", err)
	}
	return string(b), nil
}

func decodeResult(s sql.NullString) (proposals.Result, error) {
	if !s.Valid {
		return proposals.Result{}, nil
	}
	var j resultJSON
	dec := json.NewDecoder(strings.NewReader(s.String))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return proposals.Result{}, fmt.Errorf("bad proposal result in database: %w", err)
	}
	r := proposals.Result{CommitID: j.CommitID, MRURL: j.MRURL, BuildID: core.BuildID(j.BuildID), Note: j.Note}
	for _, p := range j.MovedPaths {
		r.MovedPaths = append(r.MovedPaths, proposals.RelPath(p))
	}
	return r, nil
}

// stateColumns maps a state onto the columns the row keeps for it.
type stateColumns struct {
	status    core.ProposalStatus
	decidedAt sql.NullString
	attempt   int64
	failKind  sql.NullString
	result    sql.NullString
	message   string
	retryable int64
}

func columnsOf(st proposals.State, redact func(string) string) (stateColumns, error) {
	c := stateColumns{}
	switch s := st.(type) {
	case proposals.Pending:
		c.status = core.ProposalPending
	case proposals.Confirmed:
		c.status, c.attempt = core.ProposalConfirmed, int64(s.Attempt)
		c.decidedAt = fmtNullTime(&s.At)
		c.message = redact(s.By) // who confirmed
	case proposals.Rejected:
		c.status = core.ProposalRejected
		c.decidedAt = fmtNullTime(&s.At)
	case proposals.Executed:
		c.status = core.ProposalExecuted
		c.decidedAt = fmtNullTime(&s.At)
		res, err := encodeResult(s.Result, redact)
		if err != nil {
			return c, err
		}
		c.result = sql.NullString{String: res, Valid: true}
	case proposals.Failed:
		if err := s.Kind.Validate(); err != nil {
			return c, err
		}
		c.status, c.attempt = core.ProposalFailed, int64(s.Attempt)
		c.decidedAt = fmtNullTime(&s.At)
		c.failKind = sql.NullString{String: string(s.Kind), Valid: true}
		// Free text from git or a host: redacted again here (invariant 4).
		c.message = redact(s.Message)
		c.retryable = b2i(s.Retryable)
	default:
		return c, fmt.Errorf("proposal has no state to store")
	}
	return c, nil
}

func requireTime(what string, t time.Time) error {
	return needTime(what, t)
}

func stateTime(st proposals.State) time.Time {
	switch s := st.(type) {
	case proposals.Confirmed:
		return s.At
	case proposals.Rejected:
		return s.At
	case proposals.Executed:
		return s.At
	case proposals.Failed:
		return s.At
	}
	return time.Time{}
}

// EnsureChangeSet returns the task's change set, creating it with id when the
// task has none. All proposals of a task share one (RV-03, P11). Call it in
// the same Commit that creates the proposal.
func (tx *Tx) EnsureChangeSet(ctx context.Context, task core.TaskID, id core.ChangeSetID, now time.Time) (core.ChangeSetID, error) {
	row, err := tx.q.GetFirstChangeSetByTask(ctx, string(task))
	switch {
	case err == nil:
		return core.ChangeSetID(row.ID), nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("find change set of task %s: %w", task, err)
	}
	cs, err := tx.CreateChangeSet(ctx, core.ChangeSet{ID: id, TaskID: task, Status: core.ChangeSetOpen, CreatedAt: now})
	if err != nil {
		return "", err
	}
	return cs.ID, nil
}

// CountPendingProposals counts the pending proposals of a task inside the
// transaction, for the per-task cap.
func (tx *Tx) CountPendingProposals(ctx context.Context, task core.TaskID) (int, error) {
	n, err := tx.q.CountPendingProposalsByTask(ctx, sql.NullString{String: string(task), Valid: true})
	if err != nil {
		return 0, fmt.Errorf("count pending proposals: %w", err)
	}
	return int(n), nil
}

// CreateProposalRecord stores a new pending proposal. The proposal must come
// out of proposals.Step(Created): its hash is checked against its action. The
// caller writes the audit entry in the same Commit (invariant 8).
func (tx *Tx) CreateProposalRecord(ctx context.Context, p proposals.Proposal) error {
	if p.Action == nil {
		return errors.New("create proposal: no action")
	}
	if _, ok := p.State.(proposals.Pending); !ok {
		return errors.New("create proposal: a new proposal must be pending")
	}
	if p.ActionHash != proposals.Hash(p.Action) {
		return errors.New("create proposal: the action hash does not match the action")
	}
	if err := needTime("proposal", p.Created); err != nil {
		return err
	}
	kind, payload := proposals.RowOf(p.Action)
	source := "user"
	taskCol := sql.NullString{String: string(p.Task), Valid: p.Task != ""}
	if _, ok := p.Source.(proposals.AgentSource); ok {
		source = "agent"
	}
	cs := p.ChangeSet
	err := tx.q.CreateProposalRecord(ctx, gen.CreateProposalRecordParams{
		ID: string(p.ID), ChangeSetID: nullStr(&cs), TaskID: taskCol, RepoID: sql.NullString{String: string(p.Repo), Valid: p.Repo != ""},
		Kind: string(kind), Status: string(core.ProposalPending), Summary: tx.redact(p.Summary), Payload: string(payload),
		CreatedAt: fmtTime(p.Created.UTC().Truncate(time.Millisecond)), ActionHash: p.ActionHash, Source: source, Retryable: 0,
	})
	if err != nil {
		return fmt.Errorf("create proposal: %w", err)
	}
	return nil
}

// SaveProposalRecord stores the state of p if the row is still at
// expectVersion, and returns the new version. A stale version returns
// ErrConflict. The action and its hash are never written: they cannot change.
func (tx *Tx) SaveProposalRecord(ctx context.Context, p proposals.Proposal, expectVersion int64) (int64, error) {
	cols, err := columnsOf(p.State, tx.redact)
	if err != nil {
		return 0, fmt.Errorf("save proposal %s: %w", p.ID, err)
	}
	if cols.status != core.ProposalPending {
		if err := requireTime("proposal decision", stateTime(p.State)); err != nil {
			return 0, err
		}
	}
	n, err := tx.q.SaveProposalRecord(ctx, gen.SaveProposalRecordParams{
		Status: string(cols.status), DecidedAt: cols.decidedAt, Column3: cols.attempt, FailKind: cols.failKind, // Column3 is the attempt; the query never lowers it
		Result: cols.result, Message: cols.message, Retryable: cols.retryable, ID: string(p.ID), Version: expectVersion,
	})
	return bumped("save proposal "+string(p.ID), n, err, expectVersion, func() error {
		_, err := tx.q.GetProposal(ctx, string(p.ID))
		return err
	})
}

func recordFrom(r gen.Proposal) (ProposalRecord, error) {
	kind := core.ProposalKind(r.Kind)
	act, err := proposals.ActionFromRow(kind, []byte(r.Payload))
	if err != nil {
		return ProposalRecord{}, fmt.Errorf("proposal %s in the database is not valid: %w", r.ID, err)
	}
	if r.ActionHash != "" && r.ActionHash != proposals.Hash(act) {
		return ProposalRecord{}, fmt.Errorf("proposal %s in the database has an action that does not match its hash", r.ID)
	}
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return ProposalRecord{}, err
	}
	decided, err := parseNullTime(r.DecidedAt)
	if err != nil {
		return ProposalRecord{}, err
	}
	at := time.Time{}
	if decided != nil {
		at = *decided
	}
	p := proposals.Proposal{
		ID: core.ProposalID(r.ID), Action: act, ActionHash: r.ActionHash, Summary: r.Summary, Created: created,
	}
	if r.ChangeSetID.Valid {
		p.ChangeSet = core.ChangeSetID(r.ChangeSetID.String)
	}
	if r.TaskID.Valid {
		p.Task = core.TaskID(r.TaskID.String)
	}
	if r.RepoID.Valid {
		p.Repo = core.RepoID(r.RepoID.String)
	}
	switch r.Source {
	case "agent":
		p.Source = proposals.AgentSource{Task: p.Task}
	case "user":
		p.Source = proposals.UserSource{}
	default:
		return ProposalRecord{}, fmt.Errorf("proposal %s has unknown source %q", r.ID, r.Source)
	}
	switch core.ProposalStatus(r.Status) {
	case core.ProposalPending:
		p.State = proposals.Pending{}
	case core.ProposalConfirmed:
		p.State = proposals.Confirmed{Attempt: int(r.Attempt), At: at, By: r.Message}
	case core.ProposalRejected:
		p.State = proposals.Rejected{At: at}
	case core.ProposalExecuted:
		res, err := decodeResult(r.Result)
		if err != nil {
			return ProposalRecord{}, err
		}
		p.State = proposals.Executed{At: at, Result: res}
	case core.ProposalFailed:
		k := proposals.FailKind(r.FailKind.String)
		if err := k.Validate(); err != nil {
			return ProposalRecord{}, fmt.Errorf("proposal %s: %w", r.ID, err)
		}
		p.State = proposals.Failed{Attempt: int(r.Attempt), At: at, Kind: k, Message: r.Message, Retryable: r.Retryable == 1}
	default:
		return ProposalRecord{}, fmt.Errorf("proposal %s has unknown status %q", r.ID, r.Status)
	}
	return ProposalRecord{Proposal: p, Version: r.Version}, nil
}

func recordsFrom(rows []gen.Proposal) ([]ProposalRecord, error) {
	out := make([]ProposalRecord, 0, len(rows))
	for _, r := range rows {
		rec, err := recordFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// GetProposalRecord loads one proposal as a core value. A row whose payload or
// hash does not parse is an error, never a half-loaded proposal.
func (s *Store) GetProposalRecord(ctx context.Context, id core.ProposalID) (ProposalRecord, error) {
	r, err := s.db.reader().GetProposal(ctx, string(id))
	if err := found("get proposal "+string(id), err); err != nil {
		return ProposalRecord{}, err
	}
	return recordFrom(r)
}

// ListProposalRecords loads the proposals with a status, oldest first.
func (s *Store) ListProposalRecords(ctx context.Context, status core.ProposalStatus) ([]ProposalRecord, error) {
	if err := status.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.reader().ListProposalsByStatus(ctx, string(status))
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	return recordsFrom(rows)
}

// ListTaskProposalRecords loads the proposals of a task, oldest first.
func (s *Store) ListTaskProposalRecords(ctx context.Context, task core.TaskID) ([]ProposalRecord, error) {
	rows, err := s.db.reader().ListProposalsByTask(ctx, sql.NullString{String: string(task), Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list proposals of task %s: %w", task, err)
	}
	return recordsFrom(rows)
}

// GetPendingProposalCount is the number of pending proposals a task has.
func (s *Store) GetPendingProposalCount(ctx context.Context, task core.TaskID) (int, error) {
	n, err := s.db.reader().CountPendingProposalsByTask(ctx, sql.NullString{String: string(task), Valid: true})
	if err != nil {
		return 0, fmt.Errorf("count pending proposals: %w", err)
	}
	return int(n), nil
}

// GetTaskChangeSetID returns the task's change set, or "" when it has none.
func (s *Store) GetTaskChangeSetID(ctx context.Context, task core.TaskID) (core.ChangeSetID, error) {
	row, err := s.db.reader().GetFirstChangeSetByTask(ctx, string(task))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get change set of task %s: %w", task, err)
	}
	return core.ChangeSetID(row.ID), nil
}
