package tasks

import (
	"context"
	"errors"
	"fmt"

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
		return fmt.Errorf("load active tasks: %w", err)
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
				return fmt.Errorf("recover task %s: %w", id, err)
			}
		}
	}
	if !s.spawn(func() { s.loop(s.ctx) }) {
		return errors.New("tasks: the service is closed")
	}
	s.wakeLoop()
	return nil
}

// recoverTask feeds Recovered to one live task. The usage of the turn is
// recomputed from the events the run stored before the process stopped.
func (s *Service) recoverTask(ctx context.Context, id core.TaskID) error {
	events, err := s.c.Store.ListTaskEvents(ctx, id, 0)
	if err != nil {
		return err
	}
	u, skipped := SumUsage(events)
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
