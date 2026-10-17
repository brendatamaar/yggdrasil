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

// Commit runs fn in one write transaction. If fn returns an error or panics,
// or the commit fails, nothing is written. A UNIQUE or PRIMARY KEY violation comes
// back as ErrConflict and a FOREIGN KEY violation as ErrNotFound. fn must use only the Tx it gets: calling
// the Store from inside fn would wait forever for the one write connection.
func (s *Store) Commit(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// A panic in fn must not leave the transaction open: the one write
	// connection would stay held and every later Commit would wait forever.
	// Roll back and let the panic go on.
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&Tx{q: gen.New(tx)}); err != nil {
		finished = true
		return errors.Join(classify(err), tx.Rollback())
	}
	finished = true
	if err := tx.Commit(); err != nil {
		return classify(fmt.Errorf("commit transaction: %w", err))
	}
	return nil
}

// needTime rejects a zero time: the store never reads the clock, so a caller
// that forgot one gets an error and not "now".
func needTime(what string, ts ...time.Time) error {
	for _, t := range ts {
		if t.IsZero() {
			return fmt.Errorf("%s needs a time from the caller; the store does not read the clock", what)
		}
	}
	return nil
}

// Projects

// CreateProject stores p. The caller supplies the ID and CreatedAt: the store
// neither generates IDs nor reads the clock.
func (tx *Tx) CreateProject(ctx context.Context, p core.Project) (core.Project, error) {
	if err := needTime("project", p.CreatedAt); err != nil {
		return p, err
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

// Repos

// CreateRepo stores r. The caller supplies the ID and CreatedAt.
func (tx *Tx) CreateRepo(ctx context.Context, r core.Repo) (core.Repo, error) {
	if err := needTime("repo", r.CreatedAt); err != nil {
		return r, err
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

// SetRepoTrusted records the user's answer to the repo-hooks prompt (SF-09).
func (tx *Tx) SetRepoTrusted(ctx context.Context, id core.RepoID, trusted bool) error {
	n, err := tx.q.SetRepoTrusted(ctx, gen.SetRepoTrustedParams{Trusted: b2i(trusted), ID: string(id)})
	return updated("set repo trusted "+string(id), n, err)
}

// Tasks

// CreateTask validates and stores t with its repo scope. The caller supplies
// the ID, CreatedAt and UpdatedAt; an empty status becomes draft. A sub-task
// of a sub-task returns ErrSubTaskDepth.
func (tx *Tx) CreateTask(ctx context.Context, t core.Task) (core.Task, error) {
	if t.Status == "" {
		t.Status = core.TaskDraft
	}
	if err := needTime("task", t.CreatedAt, t.UpdatedAt); err != nil {
		return t, err
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

// UpdateTaskStatus sets the status if the task is still at version; now is
// the update time, and finished_at when the status is terminal. It returns
// the new version. A stale version returns ErrConflict.
func (tx *Tx) UpdateTaskStatus(ctx context.Context, id core.TaskID, status core.TaskStatus, version int64, now time.Time) (int64, error) {
	if err := status.Validate(); err != nil {
		return 0, err
	}
	if err := needTime("task update", now); err != nil {
		return 0, err
	}
	var fin *time.Time
	if status.Terminal() {
		fin = &now
	}
	n, err := tx.q.UpdateTaskStatus(ctx, gen.UpdateTaskStatusParams{
		Status: string(status), UpdatedAt: fmtTime(now), FinishedAt: fmtNullTime(fin), ID: string(id), Version: version,
	})
	return bumped("update task "+string(id), n, err, version, func() error { _, err := tx.q.GetTask(ctx, string(id)); return err })
}

// SetTaskSession records the agent's resumable session ID if the task is
// still at version (now is the update time), and returns the new version.
func (tx *Tx) SetTaskSession(ctx context.Context, id core.TaskID, session string, version int64, now time.Time) (int64, error) {
	if err := needTime("task update", now); err != nil {
		return 0, err
	}
	n, err := tx.q.UpdateTaskSession(ctx, gen.UpdateTaskSessionParams{
		SessionID: session, UpdatedAt: fmtTime(now), ID: string(id), Version: version,
	})
	return bumped("update task session "+string(id), n, err, version, func() error { _, err := tx.q.GetTask(ctx, string(id)); return err })
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

// Worktrees

// CreateWorktree stores w. The caller supplies the ID and CreatedAt; an empty
// status becomes active.
func (tx *Tx) CreateWorktree(ctx context.Context, w core.Worktree) (core.Worktree, error) {
	if w.Status == "" {
		w.Status = core.WorktreeActive
	}
	if err := needTime("worktree", w.CreatedAt); err != nil {
		return w, err
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

// UpdateWorktreeStatus sets the status of a worktree.
func (tx *Tx) UpdateWorktreeStatus(ctx context.Context, id core.WorktreeID, status core.WorktreeStatus) error {
	if err := status.Validate(); err != nil {
		return err
	}
	n, err := tx.q.UpdateWorktreeStatus(ctx, gen.UpdateWorktreeStatusParams{Status: string(status), ID: string(id)})
	return updated("update worktree "+string(id), n, err)
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

// AppendTaskEvent stores the event in its own transaction. It is the one
// write that Store exports outside Commit: the transcript is a log of what the
// agent said, not state that changes anything, so it has no audit entry by
// design (internal/archtest allows exactly this method and Commit).
func (s *Store) AppendTaskEvent(ctx context.Context, e TaskEvent) error {
	return s.Commit(ctx, func(tx *Tx) error { return tx.AppendTaskEvent(ctx, e) })
}

// Change sets

// CreateChangeSet stores c. The caller supplies the ID and CreatedAt; an empty
// status becomes open.
func (tx *Tx) CreateChangeSet(ctx context.Context, c core.ChangeSet) (core.ChangeSet, error) {
	if c.Status == "" {
		c.Status = core.ChangeSetOpen
	}
	if c.MergeOrder == nil {
		c.MergeOrder = []core.RepoID{}
	}
	if err := needTime("change set", c.CreatedAt); err != nil {
		return c, err
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

// UpdateChangeSetStatus sets the status of a change set.
func (tx *Tx) UpdateChangeSetStatus(ctx context.Context, id core.ChangeSetID, status core.ChangeSetStatus) error {
	if err := status.Validate(); err != nil {
		return err
	}
	n, err := tx.q.UpdateChangeSetStatus(ctx, gen.UpdateChangeSetStatusParams{Status: string(status), ID: string(id)})
	return updated("update change set "+string(id), n, err)
}

// Proposals

// CreateProposal stores p. The caller supplies the ID and CreatedAt; an empty
// status becomes pending.
func (tx *Tx) CreateProposal(ctx context.Context, p core.Proposal) (core.Proposal, error) {
	if p.Status == "" {
		p.Status = core.ProposalPending
	}
	if err := needTime("proposal", p.CreatedAt); err != nil {
		return p, err
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

// UpdateProposalStatus sets the status if the proposal is still at version;
// decided_at is set to decidedAt for every status except pending. It returns
// the new version. A stale version returns ErrConflict.
func (tx *Tx) UpdateProposalStatus(ctx context.Context, id core.ProposalID, status core.ProposalStatus, version int64, decidedAt time.Time) (int64, error) {
	if err := status.Validate(); err != nil {
		return 0, err
	}
	var decided *time.Time
	if status != core.ProposalPending {
		if err := needTime("proposal decision", decidedAt); err != nil {
			return 0, err
		}
		decided = &decidedAt
	}
	n, err := tx.q.UpdateProposalStatus(ctx, gen.UpdateProposalStatusParams{
		Status: string(status), DecidedAt: fmtNullTime(decided), ID: string(id), Version: version,
	})
	return bumped("update proposal "+string(id), n, err, version, func() error { _, err := tx.q.GetProposal(ctx, string(id)); return err })
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
