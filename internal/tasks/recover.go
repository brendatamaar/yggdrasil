package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// SettingMaxAgents is the settings key of maxAgents (AG-11).
const SettingMaxAgents = "maxAgents"

// Start recovers the tasks a stopped process left behind, then starts the
// scheduler loop. Call it once, before the service takes messages:
//
//  1. Phase 2 rows (queued or running with no turn) are marked interrupted in
//     one Commit with an audit entry each, before anything is loaded.
//  2. Every task whose state is not Idle or Draft is loaded. A waiting turn
//     goes back on the board in its old order. A task that was starting,
//     running or stopping gets Recovered: it becomes Idle{Failed(Interrupted)}
//     (or needs_you when it was an Ask turn that was being stopped), with the
//     usage recomputed from its stored events. Nothing is resumed or restarted.
//  3. The scheduler is woken.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return errors.New("tasks: the service was already started or closed")
	}
	s.started = true
	s.mu.Unlock()

	if max, err := s.MaxAgents(ctx); err == nil {
		s.boardInput(coretasks.MaxChanged{Max: max})
	} else {
		s.c.Log.Warn("read maxAgents", "err", err)
	}
	if n, err := s.legacyInterrupt(ctx); err != nil {
		return fmt.Errorf("mark old running tasks interrupted: %w", err)
	} else if n > 0 {
		s.c.Log.Info("marked tasks from an earlier version interrupted", "count", n)
	}
	states, err := s.c.Store.ListActiveTaskStates(ctx)
	if err != nil {
		// One row that does not load must not keep every other task from
		// starting: load them one by one and quarantine the broken ones.
		s.c.Log.Error("an active task could not be loaded; loading the tasks one by one", "err", err)
		if states, err = s.loadActiveOneByOne(ctx); err != nil {
			return fmt.Errorf("load active tasks: %w", err)
		}
	}
	for _, st := range states {
		id := st.Row.ID
		switch st.Task.State.(type) {
		case coretasks.Waiting:
			var edits []core.RepoID
			if st.Task.Mode == core.ModeCurrentBranch && len(st.Row.RepoIDs) > 0 {
				edits = st.Row.RepoIDs[:1]
			}
			s.boardInput(coretasks.Enqueued{Task: id, Mode: st.Task.Mode, EditsMain: edits, Order: s.order.Add(1)})
		default:
			if err := s.recoverTask(ctx, id); err != nil {
				// Not fatal for the others: the loop retries it.
				s.c.Log.Error("could not recover a task at start; it is kept for a retry", "task", id, "err", err)
				s.mu.Lock()
				s.stuck[id] = true
				s.mu.Unlock()
			}
		}
	}
	if !s.spawn(func() { s.loop(s.ctx) }) {
		return errors.New("tasks: the service is closed")
	}
	s.wakeLoop()
	return nil
}

// loadActiveOneByOne is the fallback of Start when the bulk load failed. It
// reads the live tasks of every project by status and loads each state alone.
// A task whose rows do not load is quarantined: marked failed with an audit
// entry (the rows are kept, nothing is deleted) and left out.
func (s *Service) loadActiveOneByOne(ctx context.Context) ([]db.TaskState, error) {
	projects, err := s.c.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var out []db.TaskState
	for _, p := range projects {
		list, err := s.c.Store.ListTasks(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			if t.Status != core.TaskQueued && t.Status != core.TaskRunning {
				continue
			}
			st, err := s.c.Store.GetTaskState(ctx, t.ID)
			if err != nil {
				s.c.Log.Error("a task could not be loaded; it is marked failed", "task", t.ID, "err", err)
				if qerr := s.quarantine(ctx, t); qerr != nil {
					return nil, fmt.Errorf("quarantine task %s: %w", t.ID, qerr)
				}
				continue
			}
			out = append(out, st)
		}
	}
	return out, nil
}

// quarantine marks a task whose rows do not load as failed (interrupted), with
// its audit entry.
func (s *Service) quarantine(ctx context.Context, t core.Task) error {
	now := s.c.Now().UTC()
	e, err := s.auditEntry("system", "task.quarantined", t.ID, map[string]any{"outcome": "failed_interrupted", "reason": "unreadable_rows"})
	if err != nil {
		return err
	}
	row := coretasks.TaskRow{Status: core.TaskFailed, IdleOutcome: coretasks.OutcomeCode(coretasks.Failed{Kind: coretasks.FailInterrupted}), IdleAt: &now}
	return s.c.Store.Commit(ctx, func(tx *db.Tx) error {
		if _, err := tx.SaveTask(ctx, t.ID, row, t.SessionID, t.Version, now); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, e)
	})
}

// recoverTask feeds Recovered to one live task. The usage of the turn is
// recomputed from the events the run stored before the process stopped.
func (s *Service) recoverTask(ctx context.Context, id core.TaskID) error {
	events, err := s.c.Store.ListTaskEvents(ctx, id, 0)
	if err != nil {
		return err
	}
	var since time.Time
	if st, err := s.snapshot(ctx, id); err == nil && st.Turn != nil && st.Turn.StartedAt != nil {
		since = *st.Turn.StartedAt
	}
	u, skipped := SumUsage(events, since)
	if skipped > 0 {
		s.c.Log.Warn("recovery skipped undecodable events", "task", id, "count", skipped)
	}
	res, err := s.apply(ctx, id, func(db.TaskState) coretasks.Input {
		return coretasks.Recovered{Now: s.c.Now().UTC()}
	}, applyOpts{finishUsage: &u})
	if err != nil {
		return err
	}
	s.interpret(id, res)
	return nil
}
