package newrepos

import (
	"context"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Retry is a fresh human confirmation to retry the stage a Stopped or Uncertain
// operation parked at. hash is the immutable spec hash the user confirmed
// again. The attempt is the parked attempt plus one, so a late result of the
// old attempt can never be taken for the retry's. The shell rechecks, read-only,
// that the stage's effect is still absent and the original preconditions hold;
// if they do not, nothing runs and the reason is explained. A rejected or
// untracked operation never retries. Nothing here is automatic.
func (s *Service) Retry(ctx context.Context, op core.OperationID, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if _, busy := s.workers[op]; busy {
		return ErrNotApplicable // a worker still owns it; wait for it to settle
	}
	rec, err := s.d.Store.GetReservation(ctx, op)
	if err != nil {
		return err
	}
	var (
		attempt int
		stage   corenewrepos.Stage
		resume  corenewrepos.Resumable
	)
	switch st := rec.Operation.State.(type) {
	case corenewrepos.Stopped:
		if st.Reason == corenewrepos.StopRejected || st.Reason == corenewrepos.StopUntracked {
			return ErrNotApplicable
		}
		attempt, stage, resume = st.Attempt, st.Stage, st.Resume
	case corenewrepos.Uncertain:
		attempt, stage, resume = st.Attempt, st.Stage, st.Resume
	case corenewrepos.Reserved, corenewrepos.AwaitingConfirm, corenewrepos.Initializing, corenewrepos.Ready,
		corenewrepos.Publishing, corenewrepos.Published:
		return ErrNotApplicable
	}
	var want string
	switch r := resume.(type) {
	case corenewrepos.ResumeInit:
		want = r.Spec.Hash()
	case corenewrepos.ResumePublish:
		want = r.Spec.Hash()
	case corenewrepos.ResumeNone:
		return ErrNotApplicable
	}
	if stage == nil {
		return ErrNotApplicable
	}
	if hash != want {
		return ErrHashMismatch
	}
	hold, err := s.preconditions(ctx, rec, stage, resume)
	if err != nil {
		return err
	}
	res, err := s.apply(ctx, op, func(db.ReservationRecord) (corenewrepos.Input, extra) {
		return corenewrepos.RetryConfirmed{Hash: hash, Attempt: attempt + 1, PreconditionsHold: hold, Now: s.d.Clock()}, extra{actor: "user"}
	})
	if err != nil {
		return err
	}
	if !res.changed {
		if len(res.explain) > 0 {
			return &RefusedError{Reason: res.explain[0]}
		}
		return ErrNotApplicable
	}
	return s.startLocked(op, res.effects)
}

// preconditions rechecks, read-only, that a retry is safe: the stage's effect
// is still absent (an exact or conflicting finding means it must be reconciled
// or inspected, not repeated), the project folder and git identity are as at
// the reservation, and a publish still has the confirmed head. A read error is
// returned, never treated as "holds".
func (s *Service) preconditions(ctx context.Context, rec db.ReservationRecord, stage corenewrepos.Stage, resume corenewrepos.Resumable) (bool, error) {
	sctx, cancel := s.stageCtx(ctx)
	defer cancel()
	switch r := resume.(type) {
	case corenewrepos.ResumeInit:
		parent, err := s.parentOf(sctx, r.Spec.Project)
		if err != nil || s.sameParent(sctx, r.Spec, parent) != nil {
			return false, nil // the project is gone or its folder changed
		}
		if stage == corenewrepos.StageCreateDirectory || stage == corenewrepos.StageFirstCommit {
			ok, err := s.d.Git.HasAuthor(sctx, parent)
			if err != nil {
				return false, fmt.Errorf("check git author: %w", err)
			}
			if !ok {
				return false, nil
			}
		}
	case corenewrepos.ResumePublish:
		_, dir, err := s.repoDir(sctx, rec)
		if err != nil {
			return false, nil // the project is gone
		}
		fc, found, err := s.d.Git.ReadFirstCommit(sctx, dir, r.Spec.Branch)
		if err != nil || !found || fc.Commit != r.Spec.ExpectedHead {
			return false, nil // the head moved or cannot be read
		}
	case corenewrepos.ResumeNone:
		return false, nil
	}
	c, err := s.check(sctx, rec, corenewrepos.Uncertain{Stage: stage, Resume: resume})
	if err != nil {
		return false, fmt.Errorf("recheck %s: %w", stage.Name(), err)
	}
	return c.outcome == corenewrepos.ReconcileAbsent, nil
}
