package tasks

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

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
	// checks are the integrity checks of a read-only turn, kept so a panic
	// can still run the After snapshot.
	checks []*checker
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

// effects collects what the commands of one step ask for, so they can run in a
// fixed order.
type effects struct {
	kill, release, wake bool
	publish             *core.TaskStatus
	spawns              []func() // prepare and run goroutines, started last
}

// dispatch records the effect of one command. It reports false for a command
// it does not know (a new core variant the shell was not taught), which
// interpret turns into a loud failure. The commands that only matter to the
// storing step (PersistTurn, FinishTurn, Audit, Reject) and the scheduler's
// Admit have nothing to do here.
func (s *Service) dispatch(id core.TaskID, c coretasks.Command, fx *effects) bool {
	switch v := c.(type) {
	case coretasks.Enqueue:
		s.boardInput(coretasks.Enqueued{Task: id, Mode: v.Mode, EditsMain: v.EditsMain, Order: s.order.Add(1)})
		fx.wake = true
	case coretasks.Requeue:
		s.boardInput(coretasks.TaskChanged{Task: id, Kind: coretasks.KindWaiting})
	case coretasks.Dequeue:
		s.boardInput(coretasks.Removed{Task: id})
		fx.wake = true
	case coretasks.PrepareRun:
		fx.spawns = append(fx.spawns, func() { s.startPrepare(id, v) })
	case coretasks.StartRun:
		fx.spawns = append(fx.spawns, func() { s.startRun(id, v) })
	case coretasks.KillRun:
		if at := s.attemptOf(id, v.Turn, v.Attempt); at != nil {
			at.cancel()
		}
	case coretasks.KillTask:
		fx.kill = true
	case coretasks.ReleaseLocks:
		fx.release = true
	case coretasks.Publish:
		p := v.Status
		fx.publish = &p
	case coretasks.PersistTurn, coretasks.FinishTurn, coretasks.Audit, coretasks.Reject, coretasks.Admit:
	default:
		return false
	}
	return true
}

// interpret performs the commands of a committed step. It never runs before
// the commit (a crash between the two is recovered at the next start) and
// never holds the task's lock, so it may feed new inputs. A panic or an
// unknown command fails the task instead of leaving it running unwatched.
func (s *Service) interpret(id core.TaskID, res applyResult) {
	if !res.Committed {
		return
	}
	if s.afterCommit != nil && s.afterCommit(id, res.Cmds) {
		return
	}
	defer func() {
		if v := recover(); v != nil {
			s.logPanic("interpret", id, v)
			s.markFailed(id)
		}
	}()
	var fx effects
	unknown := false
	for _, c := range res.Cmds {
		if !s.dispatch(id, c, &fx) {
			unknown = true
			s.c.Log.Error("unknown task command; the task is failed", "task", id, "command", fmt.Sprintf("%T", c))
		}
	}
	// The processes end before the lock goes and before the new status is
	// announced, so a client never sees a finished task with a live process.
	// Everything that ends something comes before anything that starts
	// something, so "running" can never be published after "done".
	if fx.kill {
		s.killTask(id)
	}
	if fx.release {
		s.releaseLocks(id)
		fx.wake = true
	}
	if fx.publish != nil {
		s.c.Hub.Status(StatusMessage{TaskID: id, Title: res.Row.Title, Status: *fx.publish})
	}
	for _, f := range fx.spawns {
		f()
	}
	if fx.wake {
		s.wakeLoop()
	}
	if unknown {
		s.markFailed(id)
	}
}

// markFailed stops a task the shell cannot continue (a panic, an unknown
// command): its processes end, its slot and lock are freed, and the core marks
// it failed through Recovered. If that cannot be stored, the task is kept for
// the loop's retry.
func (s *Service) markFailed(id core.TaskID) {
	s.mu.Lock()
	if s.failing[id] {
		s.mu.Unlock()
		return
	}
	s.failing[id] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.failing, id)
		s.mu.Unlock()
		if v := recover(); v != nil {
			s.logPanic("mark task failed", id, v)
		}
	}()
	s.killTask(id)
	s.releaseLocks(id)
	if err := s.recoverTask(s.bgCtx(), id); err != nil {
		s.c.Log.Error("could not mark the task failed; it is kept for recovery", "task", id, "err", err)
		s.mu.Lock()
		s.stuck[id] = true
		s.mu.Unlock()
	}
}

// guarded wraps a goroutine body so a panic fails its task and is logged.
func (s *Service) guarded(id core.TaskID, where string, f func()) func() {
	return func() {
		defer func() {
			if v := recover(); v != nil {
				s.logPanic(where, id, v)
				s.markFailed(id)
			}
		}()
		f()
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

// feedTries bounds the tries to store one input of the service itself.
const feedTries = 5

// feed applies an input made by the service itself (a result of an effect).
// A store error other than a version conflict is retried a few times with a
// growing wait (a transient error must not lose a verdict). A version conflict
// already has its reload loop in apply. If the input still cannot be stored it
// is logged and false is returned: the task stays as it was and the next start
// (or the stuck-task retry of the loop) recovers it.
func (s *Service) feed(id core.TaskID, in coretasks.Input, o applyOpts) (applyResult, bool) {
	ctx := s.bgCtx()
	var (
		res applyResult
		err error
	)
	for try := 1; try <= feedTries; try++ {
		res, err = s.safeApply(ctx, id, func(db.TaskState) coretasks.Input { return in }, o)
		if err == nil {
			s.interpret(id, res)
			return res, true
		}
		if errors.Is(err, db.ErrConflict) || errors.Is(err, errPanicked) || try == feedTries {
			break
		}
		s.c.Log.Warn("store task input failed, trying again", "task", id, "input", fmt.Sprintf("%T", in), "try", try, "err", err)
		select {
		case <-time.After(s.c.FeedRetry * time.Duration(try)):
		case <-ctx.Done():
		}
	}
	s.c.Log.Error("store task input", "task", id, "input", fmt.Sprintf("%T", in), "err", err)
	return res, false
}

// abandon is what a final input (RunFinished, Prepared, PrepareFailed) that
// could not be stored leaves: the slot, the lock and the processes are given
// back in memory so nothing else waits on a dead run, and the task is marked
// stuck. Its stored status still says running, which the loop (and the next
// start) recovers as interrupted.
func (s *Service) abandon(id core.TaskID, what string) {
	if pv, ok := s.pendingViolation(id); ok {
		// The verdict of a read-only turn is not stored yet. Keep the evidence
		// in the log too: repo-relative changed paths (capped), redacted.
		paths, reasons := pv.summary(s.c.Redactor.Redact)
		s.c.Log.Error("the end of a task step could not be stored; the task is kept for recovery; a read-only turn changed the tree and its verdict is not stored",
			"task", id, "step", what, "reasons", reasons, "changedPaths", paths)
	} else {
		s.c.Log.Error("the end of a task step could not be stored; the task is kept for recovery", "task", id, "step", what)
	}
	s.killTask(id)
	s.releaseLocks(id)
	s.mu.Lock()
	s.stuck[id] = true
	s.mu.Unlock()
	s.wakeLoop()
}

func (s *Service) anyStuck() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stuck) > 0
}

// retryStuck tries to recover the tasks abandoned by a failed store, now that
// the store may work again. No run is alive for them (abandon killed it).
func (s *Service) retryStuck() {
	s.mu.Lock()
	ids := make([]core.TaskID, 0, len(s.stuck))
	for id := range s.stuck {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if err := s.recoverTask(s.bgCtx(), id); err != nil {
			s.c.Log.Warn("recover stuck task", "task", id, "err", err)
			continue
		}
		s.mu.Lock()
		delete(s.stuck, id)
		s.mu.Unlock()
	}
}

var errPanicked = errors.New("tasks: a panic while storing a task step")

// safeApply is apply with a recover: a panic becomes an error that is not
// retried (C7).
func (s *Service) safeApply(ctx context.Context, id core.TaskID, mk func(db.TaskState) coretasks.Input, o applyOpts) (res applyResult, err error) {
	defer func() {
		if v := recover(); v != nil {
			s.logPanic("task step", id, v)
			res, err = applyResult{}, fmt.Errorf("%w: task %s", errPanicked, id)
		}
	}()
	return s.apply(ctx, id, mk, o)
}

// logPanic logs a recovered panic. The value and the stack can hold file
// contents or tokens, so both are redacted (invariant 4).
func (s *Service) logPanic(where string, id core.TaskID, v any) {
	r := s.c.Redactor
	s.c.Log.Error("panic in the task service", "where", where, "task", id,
		"panic", r.Redact(fmt.Sprint(v)), "stack", r.Redact(string(debug.Stack())))
}
