package tasks

import (
	"context"
	"fmt"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// attempt is the live try of a turn: the context that stops its preparation
// and its run. KillRun cancels it.
type attempt struct {
	turn   coretasks.TurnID
	n      coretasks.Attempt
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *Service) newAttempt(id core.TaskID, turn coretasks.TurnID, n coretasks.Attempt) *attempt {
	ctx, cancel := context.WithCancel(s.ctx)
	at := &attempt{turn: turn, n: n, ctx: ctx, cancel: cancel}
	s.mu.Lock()
	old := s.attempts[id]
	s.attempts[id] = at
	s.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	return at
}

func (s *Service) attemptOf(id core.TaskID, turn coretasks.TurnID, n coretasks.Attempt) *attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at := s.attempts[id]; at != nil && at.turn == turn && at.n == n {
		return at
	}
	return nil
}

func (s *Service) dropAttempt(id core.TaskID) {
	s.mu.Lock()
	at := s.attempts[id]
	delete(s.attempts, id)
	s.mu.Unlock()
	if at != nil {
		at.cancel()
	}
}

func (s *Service) prepMutex(id core.TaskID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.prep[id]
	if !ok {
		m = &sync.Mutex{}
		s.prep[id] = m
	}
	return m
}

// snapshot returns the task as the service last saw it.
func (s *Service) snapshot(ctx context.Context, id core.TaskID) (db.TaskState, error) {
	e := s.entryFor(id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.valid {
		return e.st, nil
	}
	return s.c.Store.GetTaskState(ctx, id)
}

// interpret performs the commands of a committed step. It never runs before
// the commit (a crash between the two is recovered at the next start) and
// never holds the task's lock, so it may feed new inputs.
func (s *Service) interpret(id core.TaskID, res applyResult) {
	if !res.Committed {
		return
	}
	if s.afterCommit != nil && s.afterCommit(id, res.Cmds) {
		return
	}
	var (
		kill, release, wake bool
		publish             *core.TaskStatus
	)
	for _, c := range res.Cmds {
		switch v := c.(type) {
		case coretasks.Enqueue:
			s.boardInput(coretasks.Enqueued{Task: id, Mode: v.Mode, EditsMain: v.EditsMain, Order: s.order.Add(1)})
			wake = true
		case coretasks.Requeue:
			s.boardInput(coretasks.TaskChanged{Task: id, Kind: coretasks.KindWaiting})
		case coretasks.Dequeue:
			s.boardInput(coretasks.Removed{Task: id})
			wake = true
		case coretasks.PrepareRun:
			s.startPrepare(id, v)
		case coretasks.StartRun:
			s.startRun(id, v)
		case coretasks.KillRun:
			if at := s.attemptOf(id, v.Turn, v.Attempt); at != nil {
				at.cancel()
			}
		case coretasks.KillTask:
			kill = true
		case coretasks.ReleaseLocks:
			release = true
		case coretasks.Publish:
			p := v.Status
			publish = &p
		}
	}
	// The processes end before the lock goes and before the new status is
	// announced, so a client never sees a finished task with a live process.
	if kill {
		s.killTask(id)
	}
	if release {
		s.releaseLocks(id)
		wake = true
	}
	if publish != nil {
		s.c.Hub.Status(StatusMessage{TaskID: id, Title: res.Row.Title, Status: *publish})
	}
	if wake {
		s.wakeLoop()
	}
}

func (s *Service) killTask(id core.TaskID) {
	if err := s.c.Killer(string(id)); err != nil {
		// The job object still ends the tree when ygg stops.
		s.c.Log.Error("kill task processes", "task", id, "err", err)
	}
}

// releaseLocks gives the main-checkout lock back, takes the task off the board
// and drops its attempt.
func (s *Service) releaseLocks(id core.TaskID) {
	s.mu.Lock()
	rel := s.releases[id]
	delete(s.releases, id)
	s.mu.Unlock()
	if rel != nil {
		rel()
	}
	s.boardInput(coretasks.Removed{Task: id})
	s.dropAttempt(id)
}

func (s *Service) boardInput(in coretasks.BoardInput) {
	s.boardMu.Lock()
	s.board, _ = coretasks.Schedule(s.board, in)
	s.boardMu.Unlock()
}

func (s *Service) wakeLoop() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// feed applies an input made by the service itself (a result of an effect).
// A failure to store it is logged: the task stays as it was and the next start
// recovers it.
func (s *Service) feed(id core.TaskID, in coretasks.Input, o applyOpts) (applyResult, bool) {
	res, err := s.apply(s.bgCtx(), id, func(db.TaskState) coretasks.Input { return in }, o)
	if err != nil {
		s.c.Log.Error("store task input", "task", id, "input", fmt.Sprintf("%T", in), "err", err)
		return res, false
	}
	s.interpret(id, res)
	return res, true
}
