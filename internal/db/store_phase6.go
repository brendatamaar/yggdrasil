package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/core/notify"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// Persistence of the Phase 6 services other than new repos (store_newrepos.go):
// proposal effect records, workspace action requests, the outbox, notification
// history and issue-import lookups. Every write is a Tx method, so state and
// its audit entry commit together (docs/AGENTS.md 5.11 step 3, invariant 8).
// Nothing here persists a token: callers pass redacted, bounded values and the
// columns have no place for credentials.

// Issue import.

// GetDraftByDedupKey returns the first task imported for an issue in a
// project, or ErrNotFound. The unique index tasks_project_dedup makes a second
// import of the same issue a conflict at the database as well.
func (s *Store) GetDraftByDedupKey(ctx context.Context, project core.ProjectID, key string) (core.Task, error) {
	q := s.db.reader()
	r, err := q.GetTaskByDedupKey(ctx, gen.GetTaskByDedupKeyParams{ProjectID: string(project), DedupKey: key})
	if err := found("get draft "+key, err); err != nil {
		return core.Task{}, err
	}
	return s.taskFrom(ctx, q, r)
}

// GetSetupTask returns the internal setup draft of a reserved repo.
func (s *Store) GetSetupTask(ctx context.Context, repo core.RepoID) (core.Task, error) {
	q := s.db.reader()
	r, err := q.GetSetupTaskByRepo(ctx, string(repo))
	if err := found("get setup task of repo "+string(repo), err); err != nil {
		return core.Task{}, err
	}
	return s.taskFrom(ctx, q, r)
}

// Proposal effects.

// EffectState is the state of one effect record. It is a closed set.
type EffectState string

const (
	// EffectIntent: written before the effect runs.
	EffectIntent EffectState = "intent"
	// EffectSucceeded: the effect happened; a receipt is stored.
	EffectSucceeded EffectState = "succeeded"
	// EffectFailed: the effect definitely did not happen.
	EffectFailed EffectState = "failed"
	// EffectUnknown: the outcome is unknown; only a read-only reconciliation
	// settles it.
	EffectUnknown EffectState = "unknown"
)

// Validate reports whether s is a known state.
func (s EffectState) Validate() error {
	switch s {
	case EffectIntent, EffectSucceeded, EffectFailed, EffectUnknown:
		return nil
	}
	return fmt.Errorf("unknown effect state %q", string(s))
}

// EffectRecord is one durable effect of a proposal. Stage is empty for a
// single-effect kind (a review comment). ReceiptKind and Receipt hold the
// sealed receipt of a succeeded effect (never a token); Marker is written
// before a remote effect; RemoteID is the host's ID of the created object.
type EffectRecord struct {
	ID          string
	Proposal    core.ProposalID
	Operation   core.OperationID
	Stage       string
	Attempt     int
	State       EffectState
	ReceiptKind string
	Receipt     json.RawMessage
	Marker      string
	RemoteID    string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// EffectID is the deterministic ID of an effect: "<proposal>#<stage>#<attempt>".
func EffectID(proposal core.ProposalID, stage string, attempt int) string {
	return fmt.Sprintf("%s#%s#%d", proposal, stage, attempt)
}

func effectFrom(r gen.ProposalEffect) (EffectRecord, error) {
	st := EffectState(r.State)
	if err := st.Validate(); err != nil {
		return EffectRecord{}, fmt.Errorf("bad effect in database: %w", err)
	}
	if st == EffectSucceeded && r.ReceiptKind == "" {
		return EffectRecord{}, errors.New("bad effect in database: a succeeded effect has no receipt")
	}
	cr, err := parseTime(r.CreatedAt)
	if err != nil {
		return EffectRecord{}, err
	}
	up, err := parseTime(r.UpdatedAt)
	if err != nil {
		return EffectRecord{}, err
	}
	rec := EffectRecord{
		ID: r.ID, Proposal: core.ProposalID(r.ProposalID), Operation: core.OperationID(r.OperationID),
		Stage: r.Stage, Attempt: int(r.Attempt), State: st, ReceiptKind: r.ReceiptKind,
		Receipt: json.RawMessage(r.Receipt), Marker: r.Marker, RemoteID: r.RemoteID, Version: r.Version,
		CreatedAt: cr, UpdatedAt: up,
	}
	if err := validateEffectProof(rec); err != nil {
		return EffectRecord{}, fmt.Errorf("bad effect in database: %w", err)
	}
	return rec, nil
}

// InsertEffect records the intent of an effect BEFORE it runs. The same
// (proposal, stage, attempt) twice is ErrConflict, so a duplicate execution of
// one attempt is refused by the database.
func (tx *Tx) InsertEffect(ctx context.Context, e EffectRecord) error {
	if err := needTime("effect", e.CreatedAt); err != nil {
		return err
	}
	if e.Attempt < 1 || e.Proposal.Validate() != nil {
		return errors.New("insert effect: an effect needs a proposal and a positive attempt")
	}
	if e.State == "" {
		e.State = EffectIntent
	}
	if err := e.State.Validate(); err != nil {
		return err
	}
	if e.ID == "" {
		e.ID = EffectID(e.Proposal, e.Stage, e.Attempt)
	}
	if err := validateEffectProof(e); err != nil {
		return err
	}
	receipt := jsonText(e.Receipt, "{}")
	at := fmtTime(e.CreatedAt)
	return tx.q.InsertEffect(ctx, gen.InsertEffectParams{
		ID: e.ID, ProposalID: string(e.Proposal), OperationID: string(e.Operation), Stage: e.Stage,
		Attempt: int64(e.Attempt), State: string(e.State), ReceiptKind: e.ReceiptKind, Receipt: receipt,
		Marker: e.Marker, RemoteID: e.RemoteID, CreatedAt: at, UpdatedAt: at,
	})
}

// SettleEffect moves an effect to a new state with its receipt and returns the
// new version, if it is still at expected (ErrConflict otherwise). Settling to
// succeeded needs a receipt kind. The caller writes state and audit in the same
// Commit.
func (tx *Tx) SettleEffect(ctx context.Context, id string, state EffectState, receiptKind string, receipt json.RawMessage, remoteID string, expected int64, now time.Time) (int64, error) {
	if err := needTime("effect update", now); err != nil {
		return 0, err
	}
	if err := state.Validate(); err != nil {
		return 0, err
	}
	if state == EffectSucceeded && receiptKind == "" {
		return 0, errors.New("settle effect: a succeeded effect needs a receipt")
	}
	row, err := tx.q.GetEffect(ctx, id)
	if err != nil {
		return 0, found("settle effect "+id, err)
	}
	current, err := effectFrom(row)
	if err != nil {
		return 0, err
	}
	if current.Version != expected || current.State == EffectSucceeded || current.State == EffectFailed {
		return 0, ErrConflict
	}
	if state == EffectIntent {
		return 0, errors.New("settlement cannot restart an effect intent")
	}
	next := current
	next.State, next.ReceiptKind, next.Receipt, next.RemoteID = state, receiptKind, receipt, remoteID
	if err := validateEffectProof(next); err != nil {
		return 0, err
	}
	n, err := tx.q.SettleEffect(ctx, gen.SettleEffectParams{
		State: string(state), ReceiptKind: receiptKind, Receipt: jsonText(receipt, "{}"), RemoteID: remoteID,
		UpdatedAt: fmtTime(now), ID: id, Version: expected,
	})
	return bumped("settle effect "+id, n, err, expected, func() error {
		_, err := tx.q.GetEffect(ctx, id)
		return err
	})
}

// GetEffect returns one effect record.
func (s *Store) GetEffect(ctx context.Context, id string) (EffectRecord, error) {
	r, err := s.db.reader().GetEffect(ctx, id)
	if err := found("get effect "+id, err); err != nil {
		return EffectRecord{}, err
	}
	return effectFrom(r)
}

// GetEffectByKey returns the effect of one attempt of a stage.
func (s *Store) GetEffectByKey(ctx context.Context, proposal core.ProposalID, stage string, attempt int) (EffectRecord, error) {
	r, err := s.db.reader().GetEffectByKey(ctx, gen.GetEffectByKeyParams{ProposalID: string(proposal), Stage: stage, Attempt: int64(attempt)})
	if err := found("get effect of proposal "+string(proposal), err); err != nil {
		return EffectRecord{}, err
	}
	return effectFrom(r)
}

func effectsFrom(rows []gen.ProposalEffect) ([]EffectRecord, error) {
	out := make([]EffectRecord, 0, len(rows))
	for _, r := range rows {
		e, err := effectFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// ListEffectsByProposal returns the effects of a proposal, oldest first.
func (s *Store) ListEffectsByProposal(ctx context.Context, proposal core.ProposalID) ([]EffectRecord, error) {
	rows, err := s.db.reader().ListEffectsByProposal(ctx, string(proposal))
	if err != nil {
		return nil, fmt.Errorf("list effects: %w", err)
	}
	return effectsFrom(rows)
}

// ListEffectsByOperation returns the stage effects of a new-repo operation.
func (s *Store) ListEffectsByOperation(ctx context.Context, op core.OperationID) ([]EffectRecord, error) {
	rows, err := s.db.reader().ListEffectsByOperation(ctx, string(op))
	if err != nil {
		return nil, fmt.Errorf("list effects: %w", err)
	}
	return effectsFrom(rows)
}

// ListOpenEffects returns effects that started and never settled (intent or
// unknown): a restart reconciles each, read-only, before anything runs again.
func (s *Store) ListOpenEffects(ctx context.Context) ([]EffectRecord, error) {
	rows, err := s.db.reader().ListOpenEffects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open effects: %w", err)
	}
	return effectsFrom(rows)
}

// Workspace action requests.

// ErrDigestMismatch is returned when a RequestID is reused with a different
// request: a key names one request for its whole life.
var ErrDigestMismatch = errors.New("the request id was used for a different request")

// ActionState is the persisted state of an action request. Idle, Preparing and
// Ready live in memory only (the preview is not durable); a row exists from
// the moment intent is persisted.
type ActionState string

const (
	ActionSubmitting ActionState = "submitting"
	ActionCompleted  ActionState = "completed"
	ActionRefused    ActionState = "refused"
	ActionCancelled  ActionState = "cancelled"
	// ActionPreparing and ActionReady are accepted so a later task may persist
	// them; the foundation never writes them.
	ActionPreparing ActionState = "preparing"
	ActionReady     ActionState = "ready"
)

// Validate reports whether s is a known state.
func (s ActionState) Validate() error {
	switch s {
	case ActionPreparing, ActionReady, ActionSubmitting, ActionCompleted, ActionRefused, ActionCancelled:
		return nil
	}
	return fmt.Errorf("unknown action state %q", string(s))
}

// ActionRecord is a stored action request: the parsed request, its digest, the
// dispatch state and the links to what it produced.
type ActionRecord struct {
	Request actions.Request
	Digest  string
	State   ActionState
	Attempt int
	// Key is the idempotency key of the dispatch ("action:<id>:<attempt>").
	Key     string
	Refusal actions.RefuseReason
	Task    core.TaskID
	Batch   string
	// Proposals are the proposals the request produced.
	Proposals []core.ProposalID
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func actionFrom(r gen.WorkspaceActionRequest, proposals []string) (ActionRecord, error) {
	req := actions.Request{
		RequestID: core.RequestID(r.RequestID), RepoID: core.RepoID(r.RepoID), Kind: actions.Kind(r.Kind), SourceID: r.SourceID,
	}
	// The agent and model are not stored: they matter at dispatch time only and
	// the digest covers them. Validate only what is stored.
	if req.RequestID.Validate() != nil || req.RepoID.Validate() != nil || req.Kind.Validate() != nil {
		return ActionRecord{}, errors.New("bad action request in database: invalid ids or kind")
	}
	st := ActionState(r.State)
	if err := st.Validate(); err != nil {
		return ActionRecord{}, fmt.Errorf("bad action request in database: %w", err)
	}
	refusal := actions.RefuseReason(r.Refusal)
	switch {
	case st == ActionRefused && refusal.Validate() != nil:
		return ActionRecord{}, errors.New("bad action request in database: a refused request has no valid reason")
	case st != ActionRefused && r.Refusal != "":
		return ActionRecord{}, errors.New("bad action request in database: only a refused request has a reason")
	case st == ActionSubmitting && r.IdempotencyKey == "":
		return ActionRecord{}, errors.New("bad action request in database: a submitting request has no key")
	}
	cr, err := parseTime(r.CreatedAt)
	if err != nil {
		return ActionRecord{}, err
	}
	up, err := parseTime(r.UpdatedAt)
	if err != nil {
		return ActionRecord{}, err
	}
	rec := ActionRecord{
		Request: req, Digest: r.Digest, State: st, Attempt: int(r.Attempt), Key: r.IdempotencyKey, Refusal: refusal,
		Batch: r.BatchID, Version: r.Version, CreatedAt: cr, UpdatedAt: up,
	}
	if r.TaskID.Valid {
		rec.Task = core.TaskID(r.TaskID.String)
	}
	for _, p := range proposals {
		rec.Proposals = append(rec.Proposals, core.ProposalID(p))
	}
	return rec, nil
}

// BeginActionRequest records a request by its RequestID. If the ID is new, the
// row is inserted and created is true. If it exists with the same digest, the
// stored record is returned unchanged (created false), so a repeated or
// double-clicked submission finds the first and never dispatches twice. If it
// exists with another digest, ErrDigestMismatch.
func (tx *Tx) BeginActionRequest(ctx context.Context, rec ActionRecord) (ActionRecord, bool, error) {
	if err := rec.Request.Validate(); err != nil {
		return ActionRecord{}, false, err
	}
	if err := needTime("action request", rec.CreatedAt); err != nil {
		return ActionRecord{}, false, err
	}
	if len(rec.Digest) != 64 {
		return ActionRecord{}, false, errors.New("begin action request: the digest must be 64 hex digits")
	}
	if rec.Digest != rec.Request.Digest() {
		return ActionRecord{}, false, errors.New("begin action request: the digest does not match the request")
	}
	if err := rec.State.Validate(); err != nil {
		return ActionRecord{}, false, err
	}
	at := fmtTime(rec.CreatedAt)
	err := tx.q.InsertActionRequest(ctx, gen.InsertActionRequestParams{
		RequestID: string(rec.Request.RequestID), Digest: rec.Digest, RepoID: string(rec.Request.RepoID),
		Kind: string(rec.Request.Kind), SourceID: rec.Request.SourceID, State: string(rec.State), Attempt: int64(rec.Attempt),
		IdempotencyKey: rec.Key, Refusal: string(rec.Refusal), TaskID: sqlNullTask(rec.Task), BatchID: rec.Batch,
		CreatedAt: at, UpdatedAt: at,
	})
	if err == nil {
		rec.Version = 0
		return rec, true, nil
	}
	if !errors.Is(classify(err), ErrConflict) {
		return ActionRecord{}, false, fmt.Errorf("begin action request: %w", err)
	}
	row, gerr := tx.q.GetActionRequest(ctx, string(rec.Request.RequestID))
	if gerr != nil {
		return ActionRecord{}, false, fmt.Errorf("begin action request: %w", gerr)
	}
	if row.Digest != rec.Digest {
		return ActionRecord{}, false, ErrDigestMismatch
	}
	links, gerr := tx.q.ListActionProposals(ctx, row.RequestID)
	if gerr != nil {
		return ActionRecord{}, false, fmt.Errorf("begin action request: %w", gerr)
	}
	existing, gerr := actionFrom(row, links)
	return existing, false, gerr
}

func sqlNullTask(t core.TaskID) sql.NullString {
	if t == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: string(t), Valid: true}
}

// SaveActionRequest stores a new state and links if the request is still at
// expected (ErrConflict otherwise) and returns the new version. Linking the
// task and proposals in the same Commit as the effect that created them is what
// makes a lost reply recoverable.
func (tx *Tx) SaveActionRequest(ctx context.Context, rec ActionRecord, expected int64, now time.Time) (int64, error) {
	if err := needTime("action request update", now); err != nil {
		return 0, err
	}
	if err := rec.State.Validate(); err != nil {
		return 0, err
	}
	id := string(rec.Request.RequestID)
	n, err := tx.q.UpdateActionRequest(ctx, gen.UpdateActionRequestParams{
		State: string(rec.State), Attempt: int64(rec.Attempt), IdempotencyKey: rec.Key, Refusal: string(rec.Refusal),
		TaskID: sqlNullTask(rec.Task), BatchID: rec.Batch, UpdatedAt: fmtTime(now), RequestID: id, Version: expected,
	})
	v, err := bumped("save action request "+id, n, err, expected, func() error {
		_, err := tx.q.GetActionRequest(ctx, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	for _, p := range rec.Proposals {
		if err := tx.q.AddActionProposal(ctx, gen.AddActionProposalParams{RequestID: id, ProposalID: string(p)}); err != nil {
			return 0, fmt.Errorf("link proposal %s to request %s: %w", p, id, err)
		}
	}
	return v, nil
}

// GetActionRequest returns one stored request with its links.
func (s *Store) GetActionRequest(ctx context.Context, id core.RequestID) (ActionRecord, error) {
	q := s.db.reader()
	r, err := q.GetActionRequest(ctx, string(id))
	if err := found("get action request "+string(id), err); err != nil {
		return ActionRecord{}, err
	}
	links, err := q.ListActionProposals(ctx, r.RequestID)
	if err != nil {
		return ActionRecord{}, fmt.Errorf("list proposals of request %s: %w", id, err)
	}
	return actionFrom(r, links)
}

// ListUnsettledActionRequests returns requests whose intent was persisted but
// whose result was not: a restart reconciles each through its durable link.
func (s *Store) ListUnsettledActionRequests(ctx context.Context) ([]ActionRecord, error) {
	q := s.db.reader()
	rows, err := q.ListUnsettledActionRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unsettled action requests: %w", err)
	}
	out := make([]ActionRecord, 0, len(rows))
	for _, r := range rows {
		links, err := q.ListActionProposals(ctx, r.RequestID)
		if err != nil {
			return nil, fmt.Errorf("list proposals of request %s: %w", r.RequestID, err)
		}
		rec, err := actionFrom(r, links)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Outbox.

// OutboxKind says what an outbox row's subject is.
type OutboxKind string

const (
	OutboxActionRequest  OutboxKind = "action_request"
	OutboxProposalEffect OutboxKind = "proposal_effect"
)

// Validate reports whether k is known.
func (k OutboxKind) Validate() error {
	switch k {
	case OutboxActionRequest, OutboxProposalEffect:
		return nil
	}
	return fmt.Errorf("unknown outbox kind %q", string(k))
}

// OutboxRow is one durable effect to run. ClaimCount above 1 on a claimed row
// means an earlier claim expired: the worker must reconcile before acting.
type OutboxRow struct {
	ID         string
	Kind       OutboxKind
	Subject    string
	Attempt    int
	Stage      string
	Payload    json.RawMessage
	DueAt      time.Time
	ClaimCount int
	Version    int64
}

// OutboxID is the deterministic ID of a row: "<subject>#<attempt>#<stage>".
func OutboxID(subject string, attempt int, stage string) string {
	return fmt.Sprintf("%s#%d#%s", subject, attempt, stage)
}

func outboxFrom(r gen.Outbox) (OutboxRow, error) {
	k := OutboxKind(r.SubjectKind)
	if err := k.Validate(); err != nil {
		return OutboxRow{}, fmt.Errorf("bad outbox row in database: %w", err)
	}
	due, err := parseTime(r.DueAt)
	if err != nil {
		return OutboxRow{}, err
	}
	return OutboxRow{
		ID: r.ID, Kind: k, Subject: r.SubjectID, Attempt: int(r.Attempt), Stage: r.Stage,
		Payload: json.RawMessage(r.Payload), DueAt: due, ClaimCount: int(r.ClaimCount), Version: r.Version,
	}, nil
}

// EnqueueOutbox inserts a row, in the same Commit as the intent it belongs to.
// The ID is deterministic, so enqueueing the same (subject, attempt, stage)
// again is a no-op and returns false.
func (tx *Tx) EnqueueOutbox(ctx context.Context, row OutboxRow, now time.Time) (bool, error) {
	if err := needTime("outbox row", now); err != nil {
		return false, err
	}
	if err := row.Kind.Validate(); err != nil {
		return false, err
	}
	if row.Subject == "" || row.Attempt < 1 {
		return false, errors.New("enqueue outbox: a row needs a subject and a positive attempt")
	}
	due := row.DueAt
	if due.IsZero() {
		due = now
	}
	n, err := tx.q.InsertOutbox(ctx, gen.InsertOutboxParams{
		ID: OutboxID(row.Subject, row.Attempt, row.Stage), SubjectKind: string(row.Kind), SubjectID: row.Subject,
		Attempt: int64(row.Attempt), Stage: row.Stage, Payload: jsonText(row.Payload, "{}"),
		DueAt: fmtTime(due), CreatedAt: fmtTime(now),
	})
	if err != nil {
		return false, fmt.Errorf("enqueue outbox: %w", err)
	}
	return n == 1, nil
}

// ClaimOutbox leases up to max due rows to worker until now+lease. A row whose
// earlier lease expired is returned again with ClaimCount > 1.
func (tx *Tx) ClaimOutbox(ctx context.Context, worker string, now time.Time, lease time.Duration, max int) ([]OutboxRow, error) {
	if err := needTime("outbox claim", now); err != nil {
		return nil, err
	}
	if worker == "" || lease <= 0 || max < 1 {
		return nil, errors.New("claim outbox: a worker, a positive lease and a positive limit are required")
	}
	rows, err := tx.q.ClaimOutbox(ctx, gen.ClaimOutboxParams{
		Worker: worker, LeaseUntil: sql.NullString{String: fmtTime(now.Add(lease)), Valid: true},
		Now: fmtTime(now), MaxRows: int64(max),
	})
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	out := make([]OutboxRow, 0, len(rows))
	for _, r := range rows {
		o, err := outboxFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// CompleteOutbox marks a claimed row done. A row not claimed by worker is
// ErrNotFound. The returned claim version fences expired claims even when a
// worker identifier is reused.
func (tx *Tx) CompleteOutbox(ctx context.Context, id, worker string, claimVersion int64, now time.Time) error {
	if err := needTime("outbox completion", now); err != nil {
		return err
	}
	n, err := tx.q.CompleteOutbox(ctx, gen.CompleteOutboxParams{
		DoneAt: sql.NullString{String: fmtTime(now), Valid: true}, ID: id, ClaimedBy: worker, ClaimVersion: claimVersion,
	})
	return updated("complete outbox "+id, n, err)
}

// ReleaseOutbox gives a claimed row back, due again at retryAt.
func (tx *Tx) ReleaseOutbox(ctx context.Context, id, worker string, claimVersion int64, retryAt time.Time) error {
	if err := needTime("outbox release", retryAt); err != nil {
		return err
	}
	n, err := tx.q.ReleaseOutbox(ctx, gen.ReleaseOutboxParams{DueAt: fmtTime(retryAt), ID: id, ClaimedBy: worker, ClaimVersion: claimVersion})
	return updated("release outbox "+id, n, err)
}

// GetOutbox returns one row.
func (s *Store) GetOutbox(ctx context.Context, id string) (OutboxRow, error) {
	r, err := s.db.reader().GetOutbox(ctx, id)
	if err := found("get outbox "+id, err); err != nil {
		return OutboxRow{}, err
	}
	return outboxFrom(r)
}

// Notices.

func noticeFrom(r gen.Notice) (notify.Notice, error) {
	at, err := parseTime(r.CreatedAt)
	if err != nil {
		return notify.Notice{}, err
	}
	n := notify.Notice{
		ID: core.NoticeID(r.ID), Task: core.TaskID(r.TaskID), TaskTitle: r.TaskTitle, Status: notify.Status(r.Status),
		Source: notify.Source{Kind: notify.SourceKind(r.SourceKind), RunID: r.SourceRun}, SourceRevision: r.SourceRevision,
		CreatedAt: at, Cursor: r.Cursor,
	}
	if err := n.Validate(); err != nil {
		return notify.Notice{}, fmt.Errorf("bad notice in database: %w", err)
	}
	return n, nil
}

// AppendNotice stores a notice, advances the high-water mark of its source to
// the notice's revision and trims history to notify.MaxRecent newest, all in
// the caller's transaction. It returns the notice with its Cursor. A revision
// not newer than the source's high-water mark (a replay, or an old transition
// whose notice was trimmed) is ErrConflict and writes nothing. The
// caller writes the audit entry in the same Commit and publishes only after
// the commit succeeds.
func (tx *Tx) AppendNotice(ctx context.Context, n notify.Notice) (notify.Notice, error) {
	if err := n.Validate(); err != nil {
		return notify.Notice{}, err
	}
	if err := needTime("notice", n.CreatedAt); err != nil {
		return notify.Notice{}, err
	}
	// The high-water mark is checked and raised FIRST, in this transaction: a
	// transition at or below the mark (a replay, or an old one whose notice was
	// already trimmed from history) is a conflict and writes nothing. History
	// retention therefore never re-enables an old notice.
	advanced, err := tx.AdvanceNoticeHighWater(ctx, notify.Key{Task: n.Task, Source: n.Source}, n.SourceRevision, n.CreatedAt)
	if err != nil {
		return notify.Notice{}, err
	}
	if !advanced {
		return notify.Notice{}, fmt.Errorf("append notice: revision %d is not newer than the high-water mark: %w", n.SourceRevision, ErrConflict)
	}
	cursor, err := tx.q.InsertNotice(ctx, gen.InsertNoticeParams{
		ID: string(n.ID), TaskID: string(n.Task), TaskTitle: n.TaskTitle, Status: string(n.Status),
		SourceKind: string(n.Source.Kind), SourceRun: n.Source.RunID, SourceRevision: n.SourceRevision,
		CreatedAt: fmtTime(n.CreatedAt),
	})
	if err != nil {
		return notify.Notice{}, fmt.Errorf("append notice: %w", err)
	}
	n.Cursor = cursor
	if _, err := tx.q.TrimNotices(ctx, int64(notify.MaxRecent)); err != nil {
		return notify.Notice{}, fmt.Errorf("trim notices: %w", err)
	}
	return n, nil
}

// AdvanceNoticeHighWater raises the durable high-water mark of a source and
// reports whether it moved. A lower or equal revision changes nothing and
// returns false (the mark never moves back). It is also the whole effect of a
// newer transition that is not notifiable ("uninteresting newer status").
func (tx *Tx) AdvanceNoticeHighWater(ctx context.Context, k notify.Key, revision int64, now time.Time) (bool, error) {
	if err := k.Source.Validate(); err != nil || k.Task.Validate() != nil || revision < 1 {
		return false, errors.New("advance high water: a valid task, source and positive revision are required")
	}
	if err := needTime("notice high water", now); err != nil {
		return false, err
	}
	n, err := tx.q.UpsertNoticeHighWater(ctx, gen.UpsertNoticeHighWaterParams{
		TaskID: string(k.Task), SourceKind: string(k.Source.Kind), SourceRun: k.Source.RunID, Revision: revision,
		UpdatedAt: fmtTime(now),
	})
	if err != nil {
		return false, fmt.Errorf("advance notice high water: %w", err)
	}
	return n > 0, nil
}

// GetNoticeHighWater returns the durable marks for Recovered.
func (s *Store) GetNoticeHighWater(ctx context.Context) (map[notify.Key]int64, error) {
	rows, err := s.db.reader().ListNoticeHighWater(ctx)
	if err != nil {
		return nil, fmt.Errorf("list notice high water: %w", err)
	}
	out := make(map[notify.Key]int64, len(rows))
	for _, r := range rows {
		k := notify.Key{Task: core.TaskID(r.TaskID), Source: notify.Source{Kind: notify.SourceKind(r.SourceKind), RunID: r.SourceRun}}
		if err := k.Source.Validate(); err != nil || r.Revision < 1 {
			return nil, errors.New("bad notice high water in database")
		}
		out[k] = r.Revision
	}
	return out, nil
}

// NoticePage is one page of notification history. Notices are newest first
// when Recent is requested and oldest first after a cursor. Oldest and Newest
// are the cursors held now (zero when empty); Gap is true when the caller's
// cursor is older than the oldest retained notice, so the in-app list must be
// refetched and no old browser toast may be replayed.
type NoticePage struct {
	Notices []notify.Notice
	Oldest  int64
	Newest  int64
	Gap     bool
}

// ListNotices returns a page of history. after == 0 means the newest `limit`
// notices (newest first); a positive cursor returns the notices after it
// (oldest first) and sets Gap when they were trimmed past that cursor. limit is
// clamped to 1..notify.MaxPage.
func (s *Store) ListNotices(ctx context.Context, after int64, limit int) (NoticePage, error) {
	if limit < 1 || limit > notify.MaxPage {
		limit = notify.MaxPage
	}
	if after < 0 {
		return NoticePage{}, errors.New("list notices: the cursor cannot be negative")
	}
	q := s.db.reader()
	var rows []gen.Notice
	var err error
	if after == 0 {
		rows, err = q.ListNoticesRecent(ctx, int64(limit))
	} else {
		rows, err = q.ListNoticesAfter(ctx, gen.ListNoticesAfterParams{Cursor: after, Limit: int64(limit)})
	}
	if err != nil {
		return NoticePage{}, fmt.Errorf("list notices: %w", err)
	}
	page := NoticePage{Notices: make([]notify.Notice, 0, len(rows))}
	for _, r := range rows {
		n, err := noticeFrom(r)
		if err != nil {
			return NoticePage{}, err
		}
		page.Notices = append(page.Notices, n)
	}
	if page.Oldest, err = q.MinNoticeCursor(ctx); err != nil {
		return NoticePage{}, fmt.Errorf("list notices: %w", err)
	}
	if page.Newest, err = q.MaxNoticeCursor(ctx); err != nil {
		return NoticePage{}, fmt.Errorf("list notices: %w", err)
	}
	page.Gap = notify.CursorGap(after, page.Oldest)
	return page, nil
}
