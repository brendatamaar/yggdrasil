package newrepos

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// worker is the durable worker of one operation. It is owned by the service's
// context (an errgroup goroutine), stops when that context is cancelled or the
// operation's own context is, and never outlives Close. There is at most one
// per operation.
type worker struct {
	cancel context.CancelFunc
}

// startWorker hands the effect commands of a committed transition to the
// operation's worker. It returns once the worker owns them; the progress comes
// back through the stored state and the hub.
func (s *Service) startWorker(op core.OperationID, effects []corenewrepos.Command) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked(op, effects)
}

func (s *Service) startLocked(op core.OperationID, effects []corenewrepos.Command) error {
	if len(effects) == 0 {
		return nil
	}
	if s.closed {
		return ErrClosed
	}
	if _, busy := s.workers[op]; busy {
		return nil
	}
	w, wctx := s.claimLocked(op)
	return s.launchLocked(op, w, wctx, effects)
}

// claimLocked reserves the one worker slot of op. s.mu must be held and the
// slot free. While a slot is claimed no other start, reconciliation or retry
// takes the operation, so a caller can release s.mu for slow reads and still
// own the operation.
func (s *Service) claimLocked(op core.OperationID) (*worker, context.Context) {
	wctx, cancel := context.WithCancel(s.ctx)
	w := &worker{cancel: cancel}
	s.workers[op] = w
	return w, wctx
}

// claim is claimLocked with the checks of a caller that holds no lock: it fails
// when the service is closing or a worker (or another claim) owns op.
func (s *Service) claim(op core.OperationID) (*worker, context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, ErrClosed
	}
	if _, busy := s.workers[op]; busy {
		return nil, nil, ErrNotApplicable // a worker still owns it; wait for it to settle
	}
	w, wctx := s.claimLocked(op)
	return w, wctx, nil
}

// release gives a claimed slot back without starting anything.
func (s *Service) release(op core.OperationID, w *worker) {
	w.cancel()
	s.mu.Lock()
	if s.workers[op] == w {
		delete(s.workers, op)
	}
	s.mu.Unlock()
}

// launchLocked starts the goroutine of a claimed slot. s.mu must be held.
func (s *Service) launchLocked(op core.OperationID, w *worker, wctx context.Context, effects []corenewrepos.Command) error {
	if s.closed {
		w.cancel()
		delete(s.workers, op)
		return ErrClosed
	}
	s.eg.Go(func() error {
		defer func() {
			w.cancel()
			s.mu.Lock()
			if s.workers[op] == w {
				delete(s.workers, op)
			}
			s.mu.Unlock()
		}()
		s.run(wctx, op, effects)
		return nil
	})
	return nil
}

// recoveryAttempts bounds how often a failed worker tries to mark its
// operation Uncertain before it leaves that to the next start.
const recoveryAttempts = 3

// run drives the effects of op. When the drive fails (a result could not be
// recorded, or the worker panicked) the operation would otherwise stay
// Initializing or Publishing with no worker: Retry refuses running states and
// nothing re-issues Recovered until a restart. So the worker feeds Recovered:
// the state becomes Uncertain and is reconciled read-only. No stage is ever
// run from this path; only the reconciliation commands Recovered returns.
func (s *Service) run(ctx context.Context, op core.OperationID, effects []corenewrepos.Command) {
	if s.drive(ctx, op, effects) || s.ctx.Err() != nil {
		return // done, or the service is closing: the next start recovers
	}
	for i := 0; i < recoveryAttempts; i++ {
		res, err := s.markUncertain(op)
		if err == nil {
			var reconcile []corenewrepos.Command
			for _, c := range res.effects {
				if rc, ok := c.(corenewrepos.ReconcileStage); ok {
					reconcile = append(reconcile, rc)
				}
			}
			s.drive(ctx, op, reconcile)
			return
		}
		s.logErr("could not mark a failed new repo worker's operation uncertain", err, "operation", op, "try", i+1)
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(s.d.RetryDelay):
		}
	}
	s.d.Logger.Error("a new repo operation stays unsettled until the next start", "operation", op)
}

// markUncertain feeds Recovered for op: a running state becomes Uncertain, any
// other state is left as it is.
func (s *Service) markUncertain(op core.OperationID) (applied, error) {
	dctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	return s.apply(dctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
		return corenewrepos.Recovered{Now: s.d.Clock()}, extra{actor: "system"}
	})
}

func (s *Service) cancelWorker(op core.OperationID) {
	s.mu.Lock()
	w := s.workers[op]
	s.mu.Unlock()
	if w != nil {
		w.cancel()
	}
}

// drive runs the effect commands of an operation one at a time. Each result is
// fed back through apply, which commits the new state before the next effect
// is chosen. A cancelled context ends the loop: a stage that was in flight has
// already been recorded as unknown (Uncertain), and the next start reconciles
// it read-only.
//
// drive reports false when the worker failed: a result could not be recorded or
// the worker panicked (logged without secrets). Cancellation and an unknown
// reconciliation are not failures.
func (s *Service) drive(ctx context.Context, op core.OperationID, queue []corenewrepos.Command) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			s.d.Logger.Error("new repo worker panicked", "operation", op,
				"panic", s.d.Redact(fmt.Sprint(r)), "stack", s.d.Redact(string(debug.Stack())))
			ok = false
		}
	}()
	for len(queue) > 0 {
		if ctx.Err() != nil {
			return true
		}
		cmd := queue[0]
		queue = queue[1:]
		var in corenewrepos.Input
		switch c := cmd.(type) {
		case corenewrepos.RunInitStage:
			in = s.runInit(ctx, c)
		case corenewrepos.RunPublishStage:
			in = s.runPublish(ctx, c)
		case corenewrepos.ReconcileStage:
			r, ok := s.runReconcile(ctx, c)
			if !ok {
				return true // unknown or cancelled: stay Uncertain, write nothing
			}
			in = r
		case corenewrepos.PersistReservationAndSetupDraft, corenewrepos.PublishPreview, corenewrepos.PersistIntent,
			corenewrepos.PersistReceipt, corenewrepos.PersistFailure, corenewrepos.Audit, corenewrepos.CancelWorker,
			corenewrepos.Explain:
			continue // records and presentation were handled by apply
		}
		// The result is recorded even when ctx was cancelled meanwhile: that is
		// how a cancelled stage becomes Uncertain instead of being forgotten.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		res, err := s.apply(dctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
			return in, extra{actor: "system"}
		})
		cancel()
		if err != nil {
			// The effect may have happened but is not recorded. Stop and tell the
			// caller: run marks the stage Uncertain and reconciles it read-only.
			s.logErr("could not record a new repo step", err, "operation", op)
			return false
		}
		if res.cancel {
			return true
		}
		queue = append(queue, res.effects...)
	}
	return true
}
