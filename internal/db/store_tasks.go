package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// TaskState is a task as the tasks service loads it: the stored row, the
// core view that tasks.Step takes, and the turn row that carries the state.
type TaskState struct {
	// Row is the task row with its repo scope and Version (the expected
	// version of the next write).
	Row core.Task
	// Task is the core view. Mode is the mode of the last turn (the row's mode
	// when the task has no turn yet). Caps is not stored: the shell sets it.
	Task tasks.Task
	// Turn is the current turn (the last one for an idle task); nil when the
	// task has none. Its Prompt is left empty: read it with GetTurnPrompt.
	Turn *tasks.TurnRow
}

func turnRowFrom(r gen.TaskTurn) (tasks.TurnRow, error) {
	mode := core.TaskMode(r.Mode)
	if err := mode.Validate(); err != nil {
		return tasks.TurnRow{}, fmt.Errorf("bad turn %s in database: %w", r.ID, err)
	}
	started, err := parseNullTime(r.StartedAt)
	if err != nil {
		return tasks.TurnRow{}, err
	}
	finished, err := parseNullTime(r.FinishedAt)
	if err != nil {
		return tasks.TurnRow{}, err
	}
	return tasks.TurnRow{
		ID: tasks.TurnID(r.ID), Seq: int(r.Seq), Mode: mode, Attempt: tasks.Attempt(r.Attempt), State: r.State,
		StartedAt: started, FinishedAt: finished, Outcome: r.Outcome.String,
		Usage: tasks.Usage{
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheWriteTokens: r.CacheWriteTokens,
			CacheReadTokens: r.CacheReadTokens, CostMicroUSD: r.CostMicroUsd, CostKnown: r.CostKnown != 0,
			DurationMS: r.DurationMs,
		},
	}, nil
}

func (s *Store) taskStateFrom(ctx context.Context, q *gen.Queries, r gen.Task) (TaskState, error) {
	row, err := s.taskFrom(ctx, q, r)
	if err != nil {
		return TaskState{}, err
	}
	return taskStateFrom(ctx, q, row, r)
}

func taskStateFrom(ctx context.Context, q *gen.Queries, row core.Task, r gen.Task) (TaskState, error) {
	out := TaskState{Row: row, Task: tasks.Task{ID: row.ID, Mode: row.Mode, SessionID: row.SessionID}}
	if last, err := q.GetLastTurn(ctx, r.ID); err == nil {
		tr, err := turnRowFrom(last)
		if err != nil {
			return TaskState{}, err
		}
		out.Task.Turns = tr.Seq
		out.Task.Mode = tr.Mode
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TaskState{}, fmt.Errorf("load last turn of task %s: %w", r.ID, err)
	}
	trow := tasks.TaskRow{Status: row.Status, IdleOutcome: r.IdleOutcome.String, IdleAt: row.FinishedAt}
	if r.CurrentTurn.Valid {
		trow.CurrentTurn = tasks.TurnID(r.CurrentTurn.String)
		cur, err := q.GetTurn(ctx, r.CurrentTurn.String)
		switch {
		case err == nil:
			tr, err := turnRowFrom(cur)
			if err != nil {
				return TaskState{}, err
			}
			out.Turn = &tr
		case !errors.Is(err, sql.ErrNoRows):
			return TaskState{}, fmt.Errorf("load turn of task %s: %w", r.ID, err)
		}
	}
	st, err := tasks.StateFromRows(trow, out.Turn)
	if err != nil {
		return TaskState{}, fmt.Errorf("task %s has inconsistent rows: %w", r.ID, err)
	}
	out.Task.State = st
	return out, nil
}

// GetTaskState returns one task with its turn state, or ErrNotFound. Rows that
// StateFromRows rejects (for example a Phase 2 queued task with no turn) are
// an error; MarkLegacyInterrupted clears those at startup.
func (s *Store) GetTaskState(ctx context.Context, id core.TaskID) (TaskState, error) {
	q := s.db.reader()
	r, err := q.GetTask(ctx, string(id))
	if err := found("get task "+string(id), err); err != nil {
		return TaskState{}, err
	}
	return s.taskStateFrom(ctx, q, r)
}

// ListActiveTaskStates returns every task whose state is not Idle or Draft:
// the waiting and the in-flight ones, oldest first. A row that does not load
// is an error naming the task.
func (s *Store) ListActiveTaskStates(ctx context.Context) ([]TaskState, error) {
	q := s.db.reader()
	rows, err := q.ListLiveTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active tasks: %w", err)
	}
	out := make([]TaskState, 0, len(rows))
	for _, r := range rows {
		ts, err := s.taskStateFrom(ctx, q, r)
		if err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, nil
}

// GetTurnPrompt returns the prompt of a turn.
func (s *Store) GetTurnPrompt(ctx context.Context, id tasks.TurnID) (string, error) {
	p, err := s.db.reader().GetTurnPrompt(ctx, string(id))
	if err := found("get turn prompt "+string(id), err); err != nil {
		return "", err
	}
	return p, nil
}

// ListTaskTurns returns the turns of a task in order.
func (s *Store) ListTaskTurns(ctx context.Context, task core.TaskID) ([]tasks.TurnRow, error) {
	rows, err := s.db.reader().ListTurnsByTask(ctx, string(task))
	if err != nil {
		return nil, fmt.Errorf("list turns: %w", err)
	}
	out := make([]tasks.TurnRow, 0, len(rows))
	for _, r := range rows {
		tr, err := turnRowFrom(r)
		if err != nil {
			return nil, err
		}
		tr.Prompt = r.Prompt
		out = append(out, tr)
	}
	return out, nil
}

// GetMaxTaskEventSeq returns the highest event sequence stored for a task (0
// for none), so the next turn continues the numbering.
func (s *Store) GetMaxTaskEventSeq(ctx context.Context, task core.TaskID) (int64, error) {
	n, err := s.db.reader().MaxTaskEventSeq(ctx, string(task))
	if err != nil {
		return 0, fmt.Errorf("max event seq of %s: %w", task, err)
	}
	return n, nil
}

// GetRepoTrust returns whether a repo is trusted and the hash of the agent
// config the user trusted ("" when none). Untracked repos are not hidden here:
// the caller already got the repo through GetRepo.
func (s *Store) GetRepoTrust(ctx context.Context, id core.RepoID) (trusted bool, hash string, err error) {
	r, err := s.db.reader().GetRepoTrust(ctx, string(id))
	if err := found("get repo trust "+string(id), err); err != nil {
		return false, "", err
	}
	return r.Trusted != 0, r.TrustHash, nil
}

// Tx writes

// LoadTask is GetTaskState inside a write transaction.
func (tx *Tx) LoadTask(ctx context.Context, id core.TaskID) (TaskState, error) {
	r, err := tx.q.GetTask(ctx, string(id))
	if err := found("get task "+string(id), err); err != nil {
		return TaskState{}, err
	}
	repos, err := tx.q.ListTaskRepos(ctx, r.ID)
	if err != nil {
		return TaskState{}, fmt.Errorf("list repos of task %s: %w", r.ID, err)
	}
	row, err := taskRowFrom(r, repos)
	if err != nil {
		return TaskState{}, err
	}
	return taskStateFrom(ctx, tx.q, row, r)
}

// taskRowFrom is taskFrom with the repo IDs already read.
func taskRowFrom(r gen.Task, repos []string) (core.Task, error) {
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return core.Task{}, err
	}
	upd, err := parseTime(r.UpdatedAt)
	if err != nil {
		return core.Task{}, err
	}
	fin, err := parseNullTime(r.FinishedAt)
	if err != nil {
		return core.Task{}, err
	}
	ids := make([]core.RepoID, len(repos))
	for i, id := range repos {
		ids[i] = core.RepoID(id)
	}
	return core.Task{
		ID: core.TaskID(r.ID), ProjectID: core.ProjectID(r.ProjectID), ParentTaskID: strPtr[core.TaskID](r.ParentTaskID),
		Title: r.Title, Prompt: r.Prompt, Scope: core.TaskScope(r.Scope), RepoIDs: ids,
		Mode: core.TaskMode(r.Mode), Agent: r.Agent, Model: r.Model, Status: core.TaskStatus(r.Status),
		SessionID: r.SessionID, CreatedAt: created, UpdatedAt: upd, FinishedAt: fin, Version: r.Version,
	}, nil
}

// SaveTask stores the state-bearing columns of a task (status, current turn,
// idle outcome, session, finish time) if the task is still at expected, and
// returns the new version. A stale version is ErrConflict, a missing task
// ErrNotFound. row comes from tasks.RowsOf (the shell may adjust the turn
// fields of an idle task). now is the update time.
func (tx *Tx) SaveTask(ctx context.Context, id core.TaskID, row tasks.TaskRow, session string, expected int64, now time.Time) (int64, error) {
	if err := row.Status.Validate(); err != nil {
		return 0, err
	}
	if err := needTime("task update", now); err != nil {
		return 0, err
	}
	var cur, idle sql.NullString
	if row.CurrentTurn != "" {
		cur = sql.NullString{String: string(row.CurrentTurn), Valid: true}
	}
	if row.IdleOutcome != "" {
		idle = sql.NullString{String: row.IdleOutcome, Valid: true}
	}
	n, err := tx.q.UpdateTaskState(ctx, gen.UpdateTaskStateParams{
		Status: string(row.Status), CurrentTurn: cur, IdleOutcome: idle, SessionID: session,
		FinishedAt: fmtNullTime(row.IdleAt), UpdatedAt: fmtTime(now), ID: string(id), Version: expected,
	})
	return bumped("save task "+string(id), n, err, expected, func() error { _, err := tx.q.GetTask(ctx, string(id)); return err })
}

// SaveTurn stores a turn row. With insert it creates the row (Seq, Mode and
// Prompt are written); otherwise it updates the state, attempt, times, outcome
// and usage of the existing row, which never change its prompt, sequence or
// mode. A duplicate (task, seq) on insert is ErrConflict.
func (tx *Tx) SaveTurn(ctx context.Context, task core.TaskID, t tasks.TurnRow, insert bool) error {
	if err := t.ID.Validate(); err != nil {
		return err
	}
	var outcome sql.NullString
	if t.Outcome != "" {
		outcome = sql.NullString{String: t.Outcome, Valid: true}
	}
	if insert {
		err := tx.q.InsertTurn(ctx, gen.InsertTurnParams{
			ID: string(t.ID), TaskID: string(task), Seq: int64(t.Seq), Mode: string(t.Mode), Prompt: t.Prompt,
			Attempt: int64(t.Attempt), State: t.State, StartedAt: fmtNullTime(t.StartedAt),
			FinishedAt: fmtNullTime(t.FinishedAt), Outcome: outcome,
			InputTokens: t.Usage.InputTokens, OutputTokens: t.Usage.OutputTokens,
			CacheWriteTokens: t.Usage.CacheWriteTokens, CacheReadTokens: t.Usage.CacheReadTokens,
			CostMicroUsd: t.Usage.CostMicroUSD, CostKnown: b2i(t.Usage.CostKnown), DurationMs: t.Usage.DurationMS,
		})
		if err != nil {
			return fmt.Errorf("insert turn %s: %w", t.ID, err)
		}
		return nil
	}
	n, err := tx.q.UpdateTurn(ctx, gen.UpdateTurnParams{
		Attempt: int64(t.Attempt), State: t.State, StartedAt: fmtNullTime(t.StartedAt),
		FinishedAt: fmtNullTime(t.FinishedAt), Outcome: outcome,
		InputTokens: t.Usage.InputTokens, OutputTokens: t.Usage.OutputTokens,
		CacheWriteTokens: t.Usage.CacheWriteTokens, CacheReadTokens: t.Usage.CacheReadTokens,
		CostMicroUsd: t.Usage.CostMicroUSD, CostKnown: b2i(t.Usage.CostKnown), DurationMs: t.Usage.DurationMS,
		ID: string(t.ID), TaskID: string(task),
	})
	return updated("update turn "+string(t.ID), n, err)
}

// LegacyTask is a task row from before turns existed that still says queued or
// running: no process of this run owns it.
type LegacyTask struct {
	ID      core.TaskID
	Version int64
}

// ListLegacyLiveTasks returns the Phase 2 rows that claim to be queued or
// running but have no turn. Startup marks them with MarkLegacyInterrupted
// before it loads anything.
func (tx *Tx) ListLegacyLiveTasks(ctx context.Context) ([]LegacyTask, error) {
	rows, err := tx.q.ListLegacyLiveTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list legacy live tasks: %w", err)
	}
	out := make([]LegacyTask, len(rows))
	for i, r := range rows {
		out[i] = LegacyTask{ID: core.TaskID(r.ID), Version: r.Version}
	}
	return out, nil
}

// MarkLegacyInterrupted ends a legacy live task as failed (interrupted) if it
// is still at version. Call it in the same Commit as the audit entry.
func (tx *Tx) MarkLegacyInterrupted(ctx context.Context, t LegacyTask, now time.Time) error {
	if err := needTime("task update", now); err != nil {
		return err
	}
	n, err := tx.q.MarkLegacyInterrupted(ctx, gen.MarkLegacyInterruptedParams{
		FinishedAt: fmtNullTime(&now), UpdatedAt: fmtTime(now), ID: string(t.ID), Version: t.Version,
	})
	_, err = bumped("mark task interrupted "+string(t.ID), n, err, t.Version, func() error {
		_, err := tx.q.GetTask(ctx, string(t.ID))
		return err
	})
	return err
}

// SetRepoTrust records the trust of a repo's agent config: trusted with the
// hash the user saw, or not trusted with an empty hash (SF-09). Call it in the
// same Commit as its audit entry.
func (tx *Tx) SetRepoTrust(ctx context.Context, id core.RepoID, trusted bool, hash string) error {
	if trusted && len(hash) != 64 {
		return errors.New("trust needs the 64-digit hash of the config the user saw")
	}
	if !trusted {
		hash = ""
	}
	n, err := tx.q.SetRepoTrust(ctx, gen.SetRepoTrustParams{Trusted: b2i(trusted), TrustHash: hash, ID: string(id)})
	return updated("set repo trust "+string(id), n, err)
}
