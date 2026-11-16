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
	wctx, cancel := context.WithCancel(s.ctx)
	w := &worker{cancel: cancel}
	s.workers[op] = w
	s.eg.Go(func() error {
		defer func() {
			cancel()
			s.mu.Lock()
			if s.workers[op] == w {
				delete(s.workers, op)
			}
			s.mu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				s.d.Logger.Error("new repo worker panicked", "operation", op,
					"panic", s.d.Redact(fmt.Sprint(r)), "stack", s.d.Redact(string(debug.Stack())))
			}
		}()
		s.drive(wctx, op, effects)
		return nil
	})
	return nil
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
func (s *Service) drive(ctx context.Context, op core.OperationID, queue []corenewrepos.Command) {
	for len(queue) > 0 {
		if ctx.Err() != nil {
			return
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
				return // unknown or cancelled: stay Uncertain, write nothing
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
			// The effect may have happened but is not recorded. Stop; the next
			// start sees the intent, marks the stage Uncertain and reconciles it.
			s.logErr("could not record a new repo step", err, "operation", op)
			return
		}
		if res.cancel {
			return
		}
		queue = append(queue, res.effects...)
	}
}
