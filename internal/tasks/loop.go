package tasks

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// loop is the scheduler: one goroutine owned by the service group. It wakes
// when something was enqueued or released, asks coretasks.Schedule who may
// start, and feeds Admitted to each of them. A slow ticker gives waiting tasks
// another chance when a main-checkout lock outside this service (a build, a git
// action) was released without telling us.
func (s *Service) loop(ctx context.Context) {
	t := time.NewTicker(s.c.LockRetry)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-t.C:
			if !s.anyWaiting() {
				continue
			}
		}
		s.scheduleOnce()
	}
}

func (s *Service) anyWaiting() bool {
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	for _, e := range s.board.Tasks {
		if e.Kind == coretasks.KindWaiting {
			return true
		}
	}
	return false
}

// scheduleOnce admits what the board allows. The board already counts an
// admitted task as active; if the task cannot be moved to Starting (it was
// cancelled meanwhile, or the store failed) it is put back or taken off, so a
// slot is never lost.
func (s *Service) scheduleOnce() {
	s.boardMu.Lock()
	nb, cmds := coretasks.Schedule(s.board, coretasks.Wake{})
	s.board = nb
	s.boardMu.Unlock()
	for _, c := range cmds {
		a, ok := c.(coretasks.Admit)
		if !ok {
			continue
		}
		s.admitTask(a.Task)
	}
}

func (s *Service) admitTask(id core.TaskID) {
	// A task that edits a main checkout somebody else holds stays Waiting: no
	// commit, no audit, no publish. The LockRetry tick or a release wake asks
	// again.
	if holder, blocked := s.mainBlocker(id); blocked {
		s.setBlocked(id, holder)
		s.boardInput(coretasks.TaskChanged{Task: id, Kind: coretasks.KindWaiting})
		return
	}
	s.setBlocked(id, "")
	res, err := s.apply(s.bgCtx(), id, func(st db.TaskState) coretasks.Input {
		if w, ok := st.Task.State.(coretasks.Waiting); ok {
			return coretasks.Admitted{Turn: w.Turn, Now: s.c.Now().UTC()}
		}
		return nil
	}, applyOpts{})
	_, starting := res.After.State.(coretasks.Starting)
	if err != nil || !starting {
		if err != nil {
			s.c.Log.Error("admit task", "task", id, "err", err)
		}
		// Back to waiting if it still waits (it keeps its place), else off the
		// board; either way the slot and the lock are free again.
		if _, waiting := res.After.State.(coretasks.Waiting); waiting || err != nil {
			s.boardInput(coretasks.TaskChanged{Task: id, Kind: coretasks.KindWaiting})
		} else {
			s.boardInput(coretasks.Removed{Task: id})
		}
		if err == nil {
			s.wakeLoop() // after an error the ticker retries, so a broken store cannot spin
		}
		return
	}
	s.interpret(id, res)
}
