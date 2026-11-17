package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// This file is the persistence of new-repo operations (NR-01 to NR-04,
// docs/design/phase-6-newrepos.md): the reserved repo row, its setup draft task,
// the reservation row that holds the sealed core/newrepos state as a code plus
// JSON, and the stage receipts. Reads parse a row back into core types and
// reject an inconsistent one (a boundary parser); writes take the Tx of
// Store.Commit so state and audit commit together.
//
// A reserved repo is invisible to every ordinary query (queries/repos.sql); the
// methods here are the narrow setup path that sees it.

// ReserveRepo is the input of Tx.ReserveRepo. Name and RelPath are the leaf;
// Path is the future absolute folder, built by the shell from the project's
// canonical path. Nothing is created on disk.
type ReserveRepo struct {
	Operation core.OperationID
	Project   core.ProjectID
	Repo      core.RepoID
	SetupTask core.TaskID
	Path      string
	Spec      newrepos.InitSpec
	// Agent names the agent of the setup draft; the draft never runs.
	Agent string
	Title string
	Now   time.Time
}

// ReserveRepo, in one transaction: checks the project is tracked and ready,
// inserts the reserved repo row, the internal setup draft task, its task-repo
// link (through the narrow setup query; the ordinary AddTaskRepo refuses a
// reserved repo) and the reservation row in state Reserved. A leaf already
// reserved or tracked in the project is ErrConflict. The caller writes the
// audit entry in the same Commit.
func (tx *Tx) ReserveRepo(ctx context.Context, in ReserveRepo) error {
	if err := needTime("reservation", in.Now); err != nil {
		return err
	}
	if err := in.Spec.Validate(); err != nil {
		return err
	}
	if in.Spec.Project != in.Project || in.Spec.Repo != in.Repo {
		return errors.New("reserve repo: the spec names another project or repo")
	}
	repo := core.Repo{ID: in.Repo, ProjectID: in.Project, Name: in.Spec.Leaf, Path: in.Path}
	if err := repo.Validate(); err != nil {
		return fmt.Errorf("reserve repo: %w", err)
	}
	if err := in.Operation.Validate(); err != nil {
		return err
	}
	if n, err := tx.q.CountTrackedProject(ctx, string(in.Project)); err != nil {
		return fmt.Errorf("reserve repo: %w", err)
	} else if n == 0 {
		return fmt.Errorf("reserve repo: project %s: %w", in.Project, ErrNotFound)
	}
	now := in.Now.UTC().Truncate(time.Millisecond)
	if err := tx.q.CreateReservedRepo(ctx, gen.CreateReservedRepoParams{
		ID: string(in.Repo), ProjectID: string(in.Project), Name: in.Spec.Leaf, Path: in.Path,
		RelPath: in.Spec.Leaf, CreatedAt: fmtTime(now),
	}); err != nil {
		return fmt.Errorf("reserve repo %s: %w", in.Spec.Leaf, err)
	}
	task := core.Task{
		ID: in.SetupTask, ProjectID: in.Project, Title: in.Title, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{in.Repo},
		Mode: core.ModeAsk, Agent: in.Agent, Status: core.TaskDraft, CreatedAt: now, UpdatedAt: now,
		Source: &core.TaskSource{Kind: core.SourceRepoSetup},
	}
	if err := task.Validate(); err != nil {
		return fmt.Errorf("reserve repo: %w", err)
	}
	if err := tx.q.CreateTask(ctx, gen.CreateTaskParams{
		ID: string(task.ID), ProjectID: string(task.ProjectID), Title: task.Title, Prompt: "", Scope: string(task.Scope),
		Mode: string(task.Mode), Agent: task.Agent, Status: string(task.Status),
		CreatedAt: fmtTime(now), UpdatedAt: fmtTime(now), SourceKind: string(core.SourceRepoSetup),
	}); err != nil {
		return fmt.Errorf("reserve repo: %w", err)
	}
	if n, err := tx.q.AddSetupTaskRepo(ctx, gen.AddSetupTaskRepoParams{TaskID: string(task.ID), RepoID: string(in.Repo)}); err != nil {
		return fmt.Errorf("reserve repo: %w", err)
	} else if n == 0 {
		return fmt.Errorf("reserve repo: %w", ErrNotFound)
	}
	spec, err := json.Marshal(in.Spec)
	if err != nil {
		return fmt.Errorf("reserve repo: %w", err)
	}
	code, stage, attempt, detail, err := encodeNewRepoState(newrepos.Reserved{Spec: in.Spec, Hash: in.Spec.Hash()})
	if err != nil {
		return err
	}
	return tx.q.InsertReservation(ctx, gen.InsertReservationParams{
		RepoID: string(in.Repo), OperationID: string(in.Operation), ProjectID: string(in.Project), Leaf: in.Spec.Leaf,
		SetupTaskID: string(in.SetupTask), Spec: string(spec), SpecHash: in.Spec.Hash(),
		State: code, Stage: stage, Attempt: int64(attempt), Detail: detail, CreatedAt: fmtTime(now), UpdatedAt: fmtTime(now),
	})
}

// MarkRepoReady is the finalize-tracking step: the reserved repo becomes an
// ordinary ready repo, exactly once. Anything else (already ready, untracked,
// unknown) is ErrNotFound. Only this method, called from the confirmed
// initialization worker, makes a reserved repo visible to agents and MCP.
func (tx *Tx) MarkRepoReady(ctx context.Context, id core.RepoID, defaultBranch string) error {
	n, err := tx.q.MarkRepoReady(ctx, gen.MarkRepoReadyParams{DefaultBranch: defaultBranch, ID: string(id)})
	return updated("mark repo ready "+string(id), n, err)
}

// GetReservedRepo returns one reserved repo. It is the only read that sees one.
func (s *Store) GetReservedRepo(ctx context.Context, id core.RepoID) (core.Repo, error) {
	r, err := s.db.reader().GetReservedRepo(ctx, string(id))
	if err := found("get reserved repo "+string(id), err); err != nil {
		return core.Repo{}, err
	}
	return repoFrom(r)
}

// ReservationRecord is a stored operation: the core Operation plus what the
// shell needs next to it.
type ReservationRecord struct {
	Operation newrepos.Operation
	// SetupTask is the internal draft that owns the proposal.
	SetupTask core.TaskID
	// Proposal is the initialization proposal, empty until created.
	Proposal core.ProposalID
	Leaf     string
	// SpecHash is the hash of the immutable InitSpec.
	SpecHash  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func reservationFrom(r gen.RepoReservation) (ReservationRecord, error) {
	var spec newrepos.InitSpec
	if err := strictJSON(r.Spec, &spec); err != nil {
		return ReservationRecord{}, fmt.Errorf("bad reservation spec in database: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return ReservationRecord{}, fmt.Errorf("bad reservation spec in database: %w", err)
	}
	if spec.Project != core.ProjectID(r.ProjectID) || spec.Repo != core.RepoID(r.RepoID) || spec.Leaf != r.Leaf {
		return ReservationRecord{}, errors.New("bad reservation in database: immutable scope differs from row")
	}
	if spec.Hash() != r.SpecHash {
		return ReservationRecord{}, errors.New("bad reservation in database: the spec does not match its hash")
	}
	st, err := decodeNewRepoState(spec, r.State, r.Stage, int(r.Attempt), r.Detail)
	if err != nil {
		return ReservationRecord{}, err
	}
	cr, err := parseTime(r.CreatedAt)
	if err != nil {
		return ReservationRecord{}, err
	}
	up, err := parseTime(r.UpdatedAt)
	if err != nil {
		return ReservationRecord{}, err
	}
	rec := ReservationRecord{
		Operation: newrepos.Operation{
			ID: core.OperationID(r.OperationID), Project: core.ProjectID(r.ProjectID), Repo: core.RepoID(r.RepoID),
			Version: r.Version, State: st,
		},
		SetupTask: core.TaskID(r.SetupTaskID), Leaf: r.Leaf, SpecHash: r.SpecHash, CreatedAt: cr, UpdatedAt: up,
	}
	if r.ProposalID.Valid {
		rec.Proposal = core.ProposalID(r.ProposalID.String)
	}
	return rec, nil
}

// GetReservation returns one operation by ID.
func (s *Store) GetReservation(ctx context.Context, op core.OperationID) (ReservationRecord, error) {
	r, err := s.db.reader().GetReservation(ctx, string(op))
	if err := found("get reservation "+string(op), err); err != nil {
		return ReservationRecord{}, err
	}
	return reservationFrom(r)
}

// GetReservationByRepo returns the operation that reserved a repo.
func (s *Store) GetReservationByRepo(ctx context.Context, repo core.RepoID) (ReservationRecord, error) {
	r, err := s.db.reader().GetReservationByRepo(ctx, string(repo))
	if err := found("get reservation of repo "+string(repo), err); err != nil {
		return ReservationRecord{}, err
	}
	return reservationFrom(r)
}

// ListUnsettledReservations returns the operations a restart must recover:
// those that were Initializing, Publishing or Uncertain.
func (s *Store) ListUnsettledReservations(ctx context.Context) ([]ReservationRecord, error) {
	rows, err := s.db.reader().ListUnsettledReservations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unsettled reservations: %w", err)
	}
	out := make([]ReservationRecord, 0, len(rows))
	for _, r := range rows {
		rec, err := reservationFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// ListReservations returns the operations of a project, oldest first.
func (s *Store) ListReservations(ctx context.Context, project core.ProjectID) ([]ReservationRecord, error) {
	rows, err := s.db.reader().ListReservationsByProject(ctx, string(project))
	if err != nil {
		return nil, fmt.Errorf("list reservations: %w", err)
	}
	out := make([]ReservationRecord, 0, len(rows))
	for _, r := range rows {
		rec, err := reservationFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// SaveReservation stores the new state of an operation if it is still at
// expected (ErrConflict otherwise) and returns the new version. proposal is the
// initialization proposal ID once it exists (empty before). The caller writes
// the audit entry in the same Commit.
func (tx *Tx) SaveReservation(ctx context.Context, op core.OperationID, st newrepos.State, proposal core.ProposalID, expected int64, now time.Time) (int64, error) {
	if err := needTime("reservation update", now); err != nil {
		return 0, err
	}
	row, err := tx.q.GetReservation(ctx, string(op))
	if err != nil {
		return 0, fmt.Errorf("save reservation: %w", err)
	}
	var immutable newrepos.InitSpec
	if err := strictJSON(row.Spec, &immutable); err != nil {
		return 0, err
	}
	if immutable.Hash() != row.SpecHash || immutable.Project != core.ProjectID(row.ProjectID) || immutable.Repo != core.RepoID(row.RepoID) || immutable.Leaf != row.Leaf {
		return 0, errors.New("save reservation: immutable scope mismatch")
	}
	if err := newrepos.ValidateState(immutable, st); err != nil {
		return 0, err
	}
	code, stage, attempt, detail, err := encodeNewRepoState(st)
	if err != nil {
		return 0, err
	}
	var prop = nullStr[core.ProposalID](nil)
	if proposal != "" {
		prop = nullStr(&proposal)
	}
	n, err := tx.q.UpdateReservation(ctx, gen.UpdateReservationParams{
		ProposalID: prop, State: code, Stage: stage, Attempt: int64(attempt), Detail: detail,
		UpdatedAt: fmtTime(now), OperationID: string(op), Version: expected,
	})
	return bumped("save reservation "+string(op), n, err, expected, func() error {
		_, err := tx.q.GetReservation(ctx, string(op))
		return err
	})
}

// The stored form of the sealed state. state codes are the CHECK list of
// repo_reservations; stage and attempt are plain columns; detail holds the
// variant data as strict JSON.

type receiptJSON struct {
	Kind           string `json:"kind"`
	Identity       string `json:"identity,omitempty"`
	GitDirIdentity string `json:"git_dir_identity,omitempty"`
	Name           string `json:"name,omitempty"`
	ContentHash    string `json:"content_hash,omitempty"`
	Commit         string `json:"commit,omitempty"`
	Tree           string `json:"tree,omitempty"`
	MessageHash    string `json:"message_hash,omitempty"`
	Author         string `json:"author,omitempty"`
	RemoteID       string `json:"remote_id,omitempty"`
	URL            string `json:"url,omitempty"`
	Head           string `json:"head,omitempty"`
}

// EncodeReceipt returns the kind code and strict JSON of a receipt. A URL in a
// receipt must be credential-free; the encoder refuses one with userinfo.
func EncodeReceipt(r newrepos.Receipt) (kind string, data string, err error) {
	if err := newrepos.ValidateReceipt(r); err != nil {
		return "", "", err
	}
	var j receiptJSON
	switch v := r.(type) {
	case newrepos.DirReceipt:
		j = receiptJSON{Kind: "dir", Identity: v.Identity}
	case newrepos.GitReceipt:
		j = receiptJSON{Kind: "git", GitDirIdentity: v.GitDirIdentity}
	case newrepos.FileReceipt:
		j = receiptJSON{Kind: "file", Name: v.Name, ContentHash: v.ContentHash}
	case newrepos.CommitReceipt:
		j = receiptJSON{Kind: "commit", Commit: v.Commit, Tree: v.Tree, MessageHash: v.MessageHash, Author: v.Author}
	case newrepos.TrackedReceipt:
		j = receiptJSON{Kind: "tracked"}
	case newrepos.RemoteReceipt:
		j = receiptJSON{Kind: "remote", RemoteID: v.RemoteID, URL: v.URL}
	case newrepos.OriginReceipt:
		j = receiptJSON{Kind: "origin", URL: v.URL}
	case newrepos.PushReceipt:
		j = receiptJSON{Kind: "push", Head: v.Head}
	default:
		return "", "", errors.New("encode receipt: unknown receipt")
	}
	if strings.Contains(j.URL, "@") {
		return "", "", errors.New("encode receipt: a receipt URL must not carry credentials")
	}
	b, err := json.Marshal(j)
	if err != nil {
		return "", "", fmt.Errorf("encode receipt: %w", err)
	}
	return j.Kind, string(b), nil
}

// DecodeReceipt parses a stored receipt, refusing an unknown kind, unknown
// field or a kind/field mismatch.
func DecodeReceipt(data string) (newrepos.Receipt, error) {
	var j receiptJSON
	if err := strictJSON(data, &j); err != nil {
		return nil, fmt.Errorf("bad receipt in database: %w", err)
	}
	if strings.Contains(j.URL, "@") {
		return nil, errors.New("bad receipt in database: the URL carries credentials")
	}
	check := func(r newrepos.Receipt) (newrepos.Receipt, error) {
		_, canonical, err := EncodeReceipt(r)
		if err != nil {
			return nil, err
		}
		var actual, expected map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &actual); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(canonical), &expected); err != nil {
			return nil, err
		}
		for key := range actual {
			if _, ok := expected[key]; !ok {
				return nil, fmt.Errorf("receipt %s has incompatible field %s", j.Kind, key)
			}
		}
		return r, nil
	}
	switch j.Kind {
	case "dir":
		return check(newrepos.DirReceipt{Identity: j.Identity})
	case "git":
		return check(newrepos.GitReceipt{GitDirIdentity: j.GitDirIdentity})
	case "file":
		return check(newrepos.FileReceipt{Name: j.Name, ContentHash: j.ContentHash})
	case "commit":
		return check(newrepos.CommitReceipt{Commit: j.Commit, Tree: j.Tree, MessageHash: j.MessageHash, Author: j.Author})
	case "tracked":
		return check(newrepos.TrackedReceipt{})
	case "remote":
		return check(newrepos.RemoteReceipt{RemoteID: j.RemoteID, URL: j.URL})
	case "origin":
		return check(newrepos.OriginReceipt{URL: j.URL})
	case "push":
		return check(newrepos.PushReceipt{Head: j.Head})
	}
	return nil, fmt.Errorf("bad receipt in database: unknown kind %q", j.Kind)
}

type resumeJSON struct {
	fields   map[string]json.RawMessage
	Kind     string                `json:"kind"`
	Publish  *newrepos.PublishSpec `json:"publish,omitempty"`
	Receipts []json.RawMessage     `json:"receipts,omitempty"`
}

type detailJSON struct {
	// Publish is the PublishSpec of publishing and published states.
	Publish *newrepos.PublishSpec `json:"publish,omitempty"`
	// Receipts are the receipts of finished stages, in order.
	Receipts []json.RawMessage `json:"receipts,omitempty"`
	// Proposal is the proposal of awaiting_confirm.
	Proposal string `json:"proposal,omitempty"`
	// Ready and Published data.
	InitialHead string `json:"initial_head,omitempty"`
	RemoteID    string `json:"remote_id,omitempty"`
	URL         string `json:"url,omitempty"`
	Head        string `json:"head,omitempty"`
	// Reason is a stop reason code or an uncertain-state explanation.
	Reason string `json:"reason,omitempty"`
	// Resume is what a retry or reconciliation continues from.
	Resume *resumeJSON `json:"resume,omitempty"`
}

func encodeReceipts(rs []newrepos.Receipt) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, r := range rs {
		_, data, err := EncodeReceipt(r)
		if err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(data))
	}
	return out, nil
}

func decodeReceipts(raw []json.RawMessage) ([]newrepos.Receipt, error) {
	var out []newrepos.Receipt
	for _, r := range raw {
		v, err := DecodeReceipt(string(r))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func encodeResume(r newrepos.Resumable) (*resumeJSON, error) {
	switch v := r.(type) {
	case nil:
		return nil, nil
	case newrepos.ResumeNone:
		return &resumeJSON{Kind: "none"}, nil
	case newrepos.ResumeInit:
		rc, err := encodeReceipts(v.Done)
		return &resumeJSON{Kind: "init", Receipts: rc}, err
	case newrepos.ResumePublish:
		rc, err := encodeReceipts(v.Done)
		spec := v.Spec
		return &resumeJSON{Kind: "publish", Publish: &spec, Receipts: rc}, err
	}
	return nil, errors.New("encode resume: unknown variant")
}

func (r *resumeJSON) UnmarshalJSON(data []byte) error {
	type plain resumeJSON
	if err := strictJSON(string(data), (*plain)(r)); err != nil {
		return err
	}
	return json.Unmarshal(data, &r.fields)
}

func rejectForeignFields(raw string, allowed ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("expected a JSON object")
	}
	for key := range fields {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("field %s does not belong to this variant", key)
		}
	}
	return nil
}

func decodeResume(spec newrepos.InitSpec, r *resumeJSON) (newrepos.Resumable, error) {
	if r == nil {
		return nil, errors.New("bad reservation in database: a stopped or uncertain operation has no resume data")
	}
	allowed := []string{"kind"}
	switch r.Kind {
	case "init":
		allowed = append(allowed, "receipts")
	case "publish":
		allowed = append(allowed, "receipts", "publish")
	case "none":
	default:
		return nil, fmt.Errorf("unknown resume kind %q", r.Kind)
	}
	raw, err := json.Marshal(r.fields)
	if err != nil {
		return nil, err
	}
	if r.fields != nil {
		if err := rejectForeignFields(string(raw), allowed...); err != nil {
			return nil, err
		}
	}
	if r.Kind == "none" && (len(r.Receipts) != 0 || r.Publish != nil) {
		return nil, errors.New("empty resume carries foreign data")
	}
	if r.Kind == "init" && r.Publish != nil {
		return nil, errors.New("initialization resume carries publish data")
	}
	rc, err := decodeReceipts(r.Receipts)
	if err != nil {
		return nil, err
	}
	switch r.Kind {
	case "none":
		return newrepos.ResumeNone{}, nil
	case "init":
		return newrepos.ResumeInit{Spec: spec, Done: rc}, nil
	case "publish":
		if r.Publish == nil {
			return nil, errors.New("bad reservation in database: publish resume without a spec")
		}
		if err := r.Publish.Validate(); err != nil {
			return nil, fmt.Errorf("bad reservation in database: %w", err)
		}
		return newrepos.ResumePublish{Spec: *r.Publish, Done: rc}, nil
	}
	return nil, fmt.Errorf("bad reservation in database: unknown resume kind %q", r.Kind)
}

// encodeNewRepoState maps a state onto the row columns.
func encodeNewRepoState(st newrepos.State) (code, stage string, attempt int, detail string, err error) {
	if err := newrepos.ValidateStateShape(st); err != nil {
		return "", "", 0, "", err
	}
	var d detailJSON
	switch v := st.(type) {
	case newrepos.Reserved:
		code = "reserved"
	case newrepos.AwaitingConfirm:
		code, d.Proposal = "awaiting_confirm", string(v.Proposal)
	case newrepos.Initializing:
		code, stage, attempt = "initializing", string(v.Stage), v.Attempt
		d.Receipts, err = encodeReceipts(v.Done)
	case newrepos.Ready:
		code, d.InitialHead = "ready", v.InitialHead
	case newrepos.Publishing:
		code, stage, attempt = "publishing", string(v.Stage), v.Attempt
		spec := v.Spec
		d.Publish = &spec
		d.Receipts, err = encodeReceipts(v.Done)
	case newrepos.Published:
		code, d.RemoteID, d.URL, d.Head = "published", v.RemoteID, v.URL, v.Head
		if strings.Contains(v.URL, "@") {
			err = errors.New("a published URL must not carry credentials")
		}
	case newrepos.Uncertain:
		code, attempt = "uncertain", v.Attempt
		if v.Stage != nil {
			stage = v.Stage.Name()
		}
		d.Reason = v.Reason
		d.Resume, err = encodeResume(v.Resume)
	case newrepos.Stopped:
		// The attempt that stopped is kept: a retry must use a greater one, so a late
		// result of the old attempt can never be taken for the retry's.
		code, attempt = "stopped", v.Attempt
		if v.Stage != nil {
			stage = v.Stage.Name()
		}
		d.Reason = string(v.Reason)
		d.Resume, err = encodeResume(v.Resume)
	default:
		err = errors.New("the operation has no state to store")
	}
	if err != nil {
		return "", "", 0, "", fmt.Errorf("encode new repo state: %w", err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", "", 0, "", fmt.Errorf("encode new repo state: %w", err)
	}
	return code, stage, attempt, string(b), nil
}

// decodeNewRepoState parses a row back into a state and rejects impossible
// combinations: a stage on a state that has none, a missing stage on one that
// needs it, a missing resume record, a non-positive attempt.
func decodeNewRepoState(spec newrepos.InitSpec, code, stage string, attempt int, detail string) (state newrepos.State, stateErr error) {
	defer func() {
		if stateErr == nil {
			stateErr = newrepos.ValidateState(spec, state)
			if stateErr != nil {
				state = nil
			}
		}
	}()
	allowed := map[string][]string{
		"reserved": {}, "awaiting_confirm": {"proposal"}, "initializing": {"receipts"},
		"ready": {"initial_head"}, "publishing": {"publish", "receipts"},
		"published": {"remote_id", "url", "head"}, "uncertain": {"reason", "resume"}, "stopped": {"reason", "resume"},
	}
	if keys, ok := allowed[code]; ok {
		if err := rejectForeignFields(detail, keys...); err != nil {
			return nil, err
		}
	}
	var d detailJSON
	if err := strictJSON(detail, &d); err != nil {
		return nil, fmt.Errorf("bad reservation detail in database: %w", err)
	}
	bad := func(msg string) (newrepos.State, error) {
		return nil, fmt.Errorf("bad reservation in database: state %q %s", code, msg)
	}
	stageless := code != "initializing" && code != "publishing" && code != "uncertain" && code != "stopped"
	if stageless && (stage != "" || attempt != 0) {
		return bad("has a stage or attempt")
	}
	switch code {
	case "reserved":
		return newrepos.Reserved{Spec: spec, Hash: spec.Hash()}, nil
	case "awaiting_confirm":
		if core.ProposalID(d.Proposal).Validate() != nil {
			return bad("has no proposal")
		}
		return newrepos.AwaitingConfirm{Spec: spec, Proposal: core.ProposalID(d.Proposal), Hash: spec.Hash()}, nil
	case "initializing":
		s := newrepos.InitStage(stage)
		if s.Validate() != nil || attempt < 1 {
			return bad("needs an init stage and an attempt")
		}
		rc, err := decodeReceipts(d.Receipts)
		if err != nil {
			return nil, err
		}
		return newrepos.Initializing{Spec: spec, Attempt: attempt, Stage: s, Done: rc}, nil
	case "ready":
		if d.InitialHead == "" {
			return bad("has no initial head")
		}
		return newrepos.Ready{Repo: spec.Repo, InitialHead: d.InitialHead}, nil
	case "publishing":
		s := newrepos.PublishStage(stage)
		if s.Validate() != nil || attempt < 1 || d.Publish == nil {
			return bad("needs a publish stage, an attempt and a spec")
		}
		if err := d.Publish.Validate(); err != nil {
			return nil, fmt.Errorf("bad reservation in database: %w", err)
		}
		rc, err := decodeReceipts(d.Receipts)
		if err != nil {
			return nil, err
		}
		return newrepos.Publishing{Spec: *d.Publish, Attempt: attempt, Stage: s, Done: rc}, nil
	case "published":
		if d.RemoteID == "" || d.Head == "" || strings.Contains(d.URL, "@") {
			return bad("needs a remote ID, a head and a credential-free URL")
		}
		return newrepos.Published{RemoteID: d.RemoteID, URL: d.URL, Head: d.Head}, nil
	case "uncertain":
		st, err := newrepos.ParseStage(stage)
		if err != nil || attempt < 1 {
			return bad("needs a stage and an attempt")
		}
		res, err := decodeResume(spec, d.Resume)
		if err != nil {
			return nil, err
		}
		return newrepos.Uncertain{Attempt: attempt, Stage: st, Reason: d.Reason, Resume: res}, nil
	case "stopped":
		reason := newrepos.StopReason(d.Reason)
		if reason.Validate() != nil {
			return bad("has an unknown stop reason")
		}
		var st newrepos.Stage
		if stage != "" {
			var err error
			if st, err = newrepos.ParseStage(stage); err != nil {
				return bad("has an unknown stage")
			}
		}
		res, err := decodeResume(spec, d.Resume)
		if err != nil {
			return nil, err
		}
		return newrepos.Stopped{Stage: st, Reason: reason, Attempt: attempt, Resume: res}, nil
	}
	return nil, fmt.Errorf("bad reservation in database: unknown state %q", code)
}

func strictJSON(s string, v any) error {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
