package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// Tx is the unit of work of Store.Commit: every typed write the owning
// packages need, all inside one BEGIN IMMEDIATE transaction. State and its
// audit entry (AppendAudit) are committed together or not at all
// (docs/AGENTS.md 5.11 step 3, invariant 8). A Tx is valid only inside the
// function passed to Commit.
type Tx struct{ q *gen.Queries }

// Commit runs fn in one write transaction. If fn returns an error, or the
// commit fails, nothing is written. A UNIQUE or PRIMARY KEY violation comes
// back as ErrConflict and a FOREIGN KEY violation as ErrNotFound. fn must use only the Tx it gets: calling
// the Store from inside fn would wait forever for the one write connection.
func (s *Store) Commit(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(&Tx{q: gen.New(tx)}); err != nil {
		return errors.Join(classify(err), tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return classify(fmt.Errorf("commit transaction: %w", err))
	}
	return nil
}

// commit1 runs fn in a Commit and returns its value: the shape of every
// single-write Store method.
func commit1[T any](ctx context.Context, s *Store, fn func(*Tx) (T, error)) (T, error) {
	var out T
	err := s.Commit(ctx, func(tx *Tx) error {
		v, err := fn(tx)
		out = v
		return err
	})
	return out, err
}

// Projects

// CreateProject stores p. An empty ID or zero CreatedAt is filled in.
func (tx *Tx) CreateProject(ctx context.Context, p core.Project) (core.Project, error) {
	if p.ID == "" {
		p.ID = core.ProjectID(NewID("prj"))
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := p.Validate(); err != nil {
		return p, err
	}
	err := tx.q.CreateProject(ctx, gen.CreateProjectParams{
		ID: string(p.ID), Name: p.Name, Path: p.Path, CreatedAt: fmtTime(p.CreatedAt),
	})
	if err != nil {
		return p, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

// CreateProject stores p in its own transaction.
func (s *Store) CreateProject(ctx context.Context, p core.Project) (core.Project, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Project, error) { return tx.CreateProject(ctx, p) })
}

// Repos

// CreateRepo stores r. An empty ID or zero CreatedAt is filled in.
func (tx *Tx) CreateRepo(ctx context.Context, r core.Repo) (core.Repo, error) {
	if r.ID == "" {
		r.ID = core.RepoID(NewID("rep"))
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	r.CreatedAt = r.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := r.Validate(); err != nil {
		return r, err
	}
	err := tx.q.CreateRepo(ctx, gen.CreateRepoParams{
		ID: string(r.ID), ProjectID: string(r.ProjectID), Name: r.Name, Path: r.Path,
		DefaultBranch: r.DefaultBranch, Trusted: b2i(r.Trusted), CreatedAt: fmtTime(r.CreatedAt),
	})
	if err != nil {
		return r, fmt.Errorf("create repo: %w", err)
	}
	return r, nil
}

// CreateRepo stores r in its own transaction.
func (s *Store) CreateRepo(ctx context.Context, r core.Repo) (core.Repo, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Repo, error) { return tx.CreateRepo(ctx, r) })
}

// SetRepoTrusted records the user's answer to the repo-hooks prompt (SF-09).
func (tx *Tx) SetRepoTrusted(ctx context.Context, id core.RepoID, trusted bool) error {
	n, err := tx.q.SetRepoTrusted(ctx, gen.SetRepoTrustedParams{Trusted: b2i(trusted), ID: string(id)})
	return updated("set repo trusted "+string(id), n, err)
}

// SetRepoTrusted records the answer in its own transaction.
func (s *Store) SetRepoTrusted(ctx context.Context, id core.RepoID, trusted bool) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.SetRepoTrusted(ctx, id, trusted) })
}

// Tasks

// CreateTask validates and stores t with its repo scope. An empty ID or zero
// times are filled in; an empty status becomes draft. A sub-task of a
// sub-task returns ErrSubTaskDepth.
func (tx *Tx) CreateTask(ctx context.Context, t core.Task) (core.Task, error) {
	if t.ID == "" {
		t.ID = core.TaskID(NewID("tsk"))
	}
	if t.Status == "" {
		t.Status = core.TaskDraft
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC().Truncate(time.Millisecond), t.UpdatedAt.UTC().Truncate(time.Millisecond)
	if err := t.Validate(); err != nil {
		return t, err
	}
	err := tx.q.CreateTask(ctx, gen.CreateTaskParams{
		ID: string(t.ID), ProjectID: string(t.ProjectID), ParentTaskID: nullStr(t.ParentTaskID),
		Title: t.Title, Prompt: t.Prompt, Scope: string(t.Scope), Mode: string(t.Mode), Agent: t.Agent,
		Model: t.Model, Status: string(t.Status), SessionID: t.SessionID,
		CreatedAt: fmtTime(t.CreatedAt), UpdatedAt: fmtTime(t.UpdatedAt), FinishedAt: fmtNullTime(t.FinishedAt),
	})
	if err != nil {
		// Inserting a task can only fire the sub-task triggers, so a trigger
		// abort here means the one-level rule (AG-24).
		if code, ok := sqliteCode(err); ok && code == codeConstraintTrigger {
			return t, fmt.Errorf("create task: %w", ErrSubTaskDepth)
		}
		return t, fmt.Errorf("create task: %w", err)
	}
	for _, r := range t.RepoIDs {
		if err := tx.q.AddTaskRepo(ctx, gen.AddTaskRepoParams{TaskID: string(t.ID), RepoID: string(r)}); err != nil {
			return t, fmt.Errorf("create task: %w", err)
		}
	}
	return t, nil
}

// CreateTask stores t in its own transaction.
func (s *Store) CreateTask(ctx context.Context, t core.Task) (core.Task, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Task, error) { return tx.CreateTask(ctx, t) })
}

// bumped finishes a versioned update (… SET …, version = version + 1 WHERE
// id = ? AND version = ?) that changed n rows. Zero rows means either the row
// is gone (ErrNotFound) or its version moved on (ErrConflict): probe tells
// which. It returns the new version.
func bumped(what string, n int64, err error, version int64, probe func() error) (int64, error) {
	if err != nil {
		return 0, fmt.Errorf("%s: %w", what, err)
	}
	if n > 0 {
		return version + 1, nil
	}
	switch perr := probe(); {
	case perr == nil:
		return 0, fmt.Errorf("%s: %w", what, ErrConflict)
	case errors.Is(perr, sql.ErrNoRows):
		return 0, fmt.Errorf("%s: %w", what, ErrNotFound)
	default:
		return 0, fmt.Errorf("%s: %w", what, perr)
	}
}

// UpdateTaskStatus sets the status if the task is still at version; finished_at
// is set when the status is terminal. It returns the new version. A stale
// version returns ErrConflict.
func (tx *Tx) UpdateTaskStatus(ctx context.Context, id core.TaskID, status core.TaskStatus, version int64) (int64, error) {
	if err := status.Validate(); err != nil {
		return 0, err
	}
	now := time.Now()
	var fin *time.Time
	if status.Terminal() {
		fin = &now
	}
	n, err := tx.q.UpdateTaskStatus(ctx, gen.UpdateTaskStatusParams{
		Status: string(status), UpdatedAt: fmtTime(now), FinishedAt: fmtNullTime(fin), ID: string(id), Version: version,
	})
	return bumped("update task "+string(id), n, err, version, func() error { _, err := tx.q.GetTask(ctx, string(id)); return err })
}

// UpdateTaskStatus sets the status in its own transaction.
func (s *Store) UpdateTaskStatus(ctx context.Context, id core.TaskID, status core.TaskStatus, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.UpdateTaskStatus(ctx, id, status, version) })
}

// SetTaskSession records the agent's resumable session ID if the task is
// still at version, and returns the new version.
func (tx *Tx) SetTaskSession(ctx context.Context, id core.TaskID, session string, version int64) (int64, error) {
	n, err := tx.q.UpdateTaskSession(ctx, gen.UpdateTaskSessionParams{
		SessionID: session, UpdatedAt: fmtTime(time.Now()), ID: string(id), Version: version,
	})
	return bumped("update task session "+string(id), n, err, version, func() error { _, err := tx.q.GetTask(ctx, string(id)); return err })
}

// SetTaskSession records the session in its own transaction.
func (s *Store) SetTaskSession(ctx context.Context, id core.TaskID, session string, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.SetTaskSession(ctx, id, session, version) })
}

// AddTaskDependency makes task start after dependsOn (AG-25). The caller
// validates that the order is acyclic.
func (tx *Tx) AddTaskDependency(ctx context.Context, task, dependsOn core.TaskID) error {
	err := tx.q.AddTaskDependency(ctx, gen.AddTaskDependencyParams{TaskID: string(task), DependsOnTaskID: string(dependsOn)})
	if err != nil {
		return fmt.Errorf("add task dependency: %w", err)
	}
	return nil
}

// AddTaskDependency stores the dependency in its own transaction.
func (s *Store) AddTaskDependency(ctx context.Context, task, dependsOn core.TaskID) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.AddTaskDependency(ctx, task, dependsOn) })
}

// Worktrees

// CreateWorktree stores w. An empty ID, status or zero CreatedAt is filled in.
func (tx *Tx) CreateWorktree(ctx context.Context, w core.Worktree) (core.Worktree, error) {
	if w.ID == "" {
		w.ID = core.WorktreeID(NewID("wt"))
	}
	if w.Status == "" {
		w.Status = core.WorktreeActive
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now()
	}
	w.CreatedAt = w.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := w.ID.Validate(); err != nil {
		return w, err
	}
	if err := w.Status.Validate(); err != nil {
		return w, err
	}
	err := tx.q.CreateWorktree(ctx, gen.CreateWorktreeParams{
		ID: string(w.ID), RepoID: string(w.RepoID), TaskID: string(w.TaskID), Name: w.Name, Path: w.Path,
		Branch: w.Branch, Status: string(w.Status), CreatedAt: fmtTime(w.CreatedAt),
	})
	if err != nil {
		return w, fmt.Errorf("create worktree: %w", err)
	}
	return w, nil
}

// CreateWorktree stores w in its own transaction.
func (s *Store) CreateWorktree(ctx context.Context, w core.Worktree) (core.Worktree, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Worktree, error) { return tx.CreateWorktree(ctx, w) })
}

// UpdateWorktreeStatus sets the status of a worktree.
func (tx *Tx) UpdateWorktreeStatus(ctx context.Context, id core.WorktreeID, status core.WorktreeStatus) error {
	if err := status.Validate(); err != nil {
		return err
	}
	n, err := tx.q.UpdateWorktreeStatus(ctx, gen.UpdateWorktreeStatusParams{Status: string(status), ID: string(id)})
	return updated("update worktree "+string(id), n, err)
}

// UpdateWorktreeStatus sets the status in its own transaction.
func (s *Store) UpdateWorktreeStatus(ctx context.Context, id core.WorktreeID, status core.WorktreeStatus) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.UpdateWorktreeStatus(ctx, id, status) })
}

// Task events

// AppendTaskEvent stores one event of a task.
func (tx *Tx) AppendTaskEvent(ctx context.Context, e TaskEvent) error {
	err := tx.q.AppendTaskEvent(ctx, gen.AppendTaskEventParams{
		TaskID: string(e.TaskID), Seq: e.Seq, Kind: e.Kind, At: fmtTime(e.At), Data: jsonText(e.Data, "{}"),
	})
	if err != nil {
		return fmt.Errorf("append task event: %w", err)
	}
	return nil
}

// AppendTaskEvent stores the event in its own transaction.
func (s *Store) AppendTaskEvent(ctx context.Context, e TaskEvent) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.AppendTaskEvent(ctx, e) })
}

// Change sets

// CreateChangeSet stores c. An empty ID, status or zero CreatedAt is filled in.
func (tx *Tx) CreateChangeSet(ctx context.Context, c core.ChangeSet) (core.ChangeSet, error) {
	if c.ID == "" {
		c.ID = core.ChangeSetID(NewID("cs"))
	}
	if c.Status == "" {
		c.Status = core.ChangeSetOpen
	}
	if c.MergeOrder == nil {
		c.MergeOrder = []core.RepoID{}
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.CreatedAt = c.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := c.ID.Validate(); err != nil {
		return c, err
	}
	if err := c.Status.Validate(); err != nil {
		return c, err
	}
	order, err := json.Marshal(c.MergeOrder)
	if err != nil {
		return c, fmt.Errorf("encode merge order: %w", err)
	}
	err = tx.q.CreateChangeSet(ctx, gen.CreateChangeSetParams{
		ID: string(c.ID), TaskID: string(c.TaskID), Status: string(c.Status), MergeOrder: string(order), CreatedAt: fmtTime(c.CreatedAt),
	})
	if err != nil {
		return c, fmt.Errorf("create change set: %w", err)
	}
	return c, nil
}

// CreateChangeSet stores c in its own transaction.
func (s *Store) CreateChangeSet(ctx context.Context, c core.ChangeSet) (core.ChangeSet, error) {
	return commit1(ctx, s, func(tx *Tx) (core.ChangeSet, error) { return tx.CreateChangeSet(ctx, c) })
}

// UpdateChangeSetStatus sets the status of a change set.
func (tx *Tx) UpdateChangeSetStatus(ctx context.Context, id core.ChangeSetID, status core.ChangeSetStatus) error {
	if err := status.Validate(); err != nil {
		return err
	}
	n, err := tx.q.UpdateChangeSetStatus(ctx, gen.UpdateChangeSetStatusParams{Status: string(status), ID: string(id)})
	return updated("update change set "+string(id), n, err)
}

// UpdateChangeSetStatus sets the status in its own transaction.
func (s *Store) UpdateChangeSetStatus(ctx context.Context, id core.ChangeSetID, status core.ChangeSetStatus) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.UpdateChangeSetStatus(ctx, id, status) })
}

// Proposals

// CreateProposal stores p. An empty ID, status or zero CreatedAt is filled in.
func (tx *Tx) CreateProposal(ctx context.Context, p core.Proposal) (core.Proposal, error) {
	if p.ID == "" {
		p.ID = core.ProposalID(NewID("prop"))
	}
	if p.Status == "" {
		p.Status = core.ProposalPending
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := p.ID.Validate(); err != nil {
		return p, err
	}
	if err := p.Kind.Validate(); err != nil {
		return p, err
	}
	if err := p.Status.Validate(); err != nil {
		return p, err
	}
	err := tx.q.CreateProposal(ctx, gen.CreateProposalParams{
		ID: string(p.ID), ChangeSetID: nullStr(p.ChangeSetID), TaskID: nullStr(p.TaskID), RepoID: nullStr(p.RepoID),
		Kind: string(p.Kind), Status: string(p.Status), Summary: p.Summary, Payload: jsonText(p.Payload, "{}"),
		CreatedAt: fmtTime(p.CreatedAt), DecidedAt: fmtNullTime(p.DecidedAt),
	})
	if err != nil {
		return p, fmt.Errorf("create proposal: %w", err)
	}
	return p, nil
}

// CreateProposal stores p in its own transaction.
func (s *Store) CreateProposal(ctx context.Context, p core.Proposal) (core.Proposal, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Proposal, error) { return tx.CreateProposal(ctx, p) })
}

// UpdateProposalStatus sets the status if the proposal is still at version;
// decided_at is set for every status except pending. It returns the new
// version. A stale version returns ErrConflict.
func (tx *Tx) UpdateProposalStatus(ctx context.Context, id core.ProposalID, status core.ProposalStatus, version int64) (int64, error) {
	if err := status.Validate(); err != nil {
		return 0, err
	}
	var decided *time.Time
	if status != core.ProposalPending {
		now := time.Now()
		decided = &now
	}
	n, err := tx.q.UpdateProposalStatus(ctx, gen.UpdateProposalStatusParams{
		Status: string(status), DecidedAt: fmtNullTime(decided), ID: string(id), Version: version,
	})
	return bumped("update proposal "+string(id), n, err, version, func() error { _, err := tx.q.GetProposal(ctx, string(id)); return err })
}

// UpdateProposalStatus sets the status in its own transaction.
func (s *Store) UpdateProposalStatus(ctx context.Context, id core.ProposalID, status core.ProposalStatus, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.UpdateProposalStatus(ctx, id, status, version) })
}

// Builds

// CreateBuild stores b and its steps. An empty ID, status or zero CreatedAt
// is filled in; step indexes are set to their position.
func (tx *Tx) CreateBuild(ctx context.Context, b core.Build) (core.Build, error) {
	if b.ID == "" {
		b.ID = core.BuildID(NewID("bld"))
	}
	if b.Status == "" {
		b.Status = core.BuildQueued
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now()
	}
	b.CreatedAt = b.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := b.ID.Validate(); err != nil {
		return b, err
	}
	if b.Steps == nil {
		b.Steps = []core.BuildStep{}
	}
	err := tx.q.CreateBuild(ctx, gen.CreateBuildParams{
		ID: string(b.ID), WorktreeID: nullStr(b.WorktreeID), TaskID: nullStr(b.TaskID), Status: string(b.Status),
		CreatedAt: fmtTime(b.CreatedAt), FinishedAt: fmtNullTime(b.FinishedAt),
	})
	if err != nil {
		return b, fmt.Errorf("create build: %w", err)
	}
	for i, st := range b.Steps {
		b.Steps[i].Index = i
		argv, err := argvText(st.Argv)
		if err != nil {
			return b, err
		}
		err = tx.q.CreateBuildStep(ctx, gen.CreateBuildStepParams{
			BuildID: string(b.ID), Idx: int64(i), RepoID: string(st.RepoID), Name: st.Name, Status: string(st.Status),
			ExitCode: nullInt(st.ExitCode), LogPath: st.LogPath, StartedAt: fmtNullTime(st.StartedAt), FinishedAt: fmtNullTime(st.FinishedAt),
			Argv: argv, Attempt: int64(st.Attempt), Outcome: nullText(st.Outcome),
		})
		if err != nil {
			return b, fmt.Errorf("create build: %w", err)
		}
	}
	return b, nil
}

// CreateBuild stores b in its own transaction.
func (s *Store) CreateBuild(ctx context.Context, b core.Build) (core.Build, error) {
	return commit1(ctx, s, func(tx *Tx) (core.Build, error) { return tx.CreateBuild(ctx, b) })
}

// UpdateBuildStatus sets the status if the build is still at version;
// finished_at is set when the build ended. It returns the new version. A
// stale version returns ErrConflict.
func (tx *Tx) UpdateBuildStatus(ctx context.Context, id core.BuildID, status core.BuildStatus, version int64) (int64, error) {
	if err := status.Validate(); err != nil {
		return 0, err
	}
	var fin *time.Time
	if status != core.BuildQueued && status != core.BuildRunning {
		now := time.Now()
		fin = &now
	}
	n, err := tx.q.UpdateBuildStatus(ctx, gen.UpdateBuildStatusParams{
		Status: string(status), FinishedAt: fmtNullTime(fin), ID: string(id), Version: version,
	})
	return bumped("update build "+string(id), n, err, version, func() error { _, err := tx.q.GetBuild(ctx, string(id)); return err })
}

// UpdateBuildStatus sets the status in its own transaction.
func (s *Store) UpdateBuildStatus(ctx context.Context, id core.BuildID, status core.BuildStatus, version int64) (int64, error) {
	return commit1(ctx, s, func(tx *Tx) (int64, error) { return tx.UpdateBuildStatus(ctx, id, status, version) })
}

// UpdateBuildStep replaces the mutable fields of step st.Index of a build.
// Steps have no version of their own: they are saved with their build.
func (tx *Tx) UpdateBuildStep(ctx context.Context, id core.BuildID, st core.BuildStep) error {
	if err := st.Status.Validate(); err != nil {
		return err
	}
	n, err := tx.q.UpdateBuildStep(ctx, gen.UpdateBuildStepParams{
		Status: string(st.Status), ExitCode: nullInt(st.ExitCode), LogPath: st.LogPath,
		StartedAt: fmtNullTime(st.StartedAt), FinishedAt: fmtNullTime(st.FinishedAt),
		Attempt: int64(st.Attempt), Outcome: nullText(st.Outcome), BuildID: string(id), Idx: int64(st.Index),
	})
	return updated(fmt.Sprintf("update build %s step %d", id, st.Index), n, err)
}

// UpdateBuildStep replaces the step in its own transaction.
func (s *Store) UpdateBuildStep(ctx context.Context, id core.BuildID, st core.BuildStep) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.UpdateBuildStep(ctx, id, st) })
}

// Audit log

// AppendAudit stores e as it is in the same transaction as the state it
// describes (invariant 8). The entry must come from audit.Prepare, which
// redacts it (invariant 4); this layer only rejects what the table could not
// hold.
func (tx *Tx) AppendAudit(ctx context.Context, e core.AuditEntry) error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if e.Actor == "" || e.Action == "" || e.Subject == "" {
		return errors.New("audit entry needs actor, action and subject")
	}
	if len(e.Detail) > 0 && !json.Valid(e.Detail) {
		return errors.New("audit detail is not valid JSON")
	}
	err := tx.q.InsertAuditEntry(ctx, gen.InsertAuditEntryParams{
		ID: string(e.ID), At: fmtTime(e.At), Actor: e.Actor, Action: e.Action, Subject: e.Subject, Detail: jsonText(e.Detail, "{}"),
	})
	if err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}
