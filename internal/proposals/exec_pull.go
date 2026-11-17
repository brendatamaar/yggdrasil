package proposals

import (
	"context"
	"errors"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// Errors a PullRunner returns, wrapped. The wiring adapter around the git-action
// service maps its outcome codes onto these; an error that wraps none of them
// and has no user message is a retryable execution error.
var (
	// ErrPullHeadChanged: the branch tip is no longer the expected one. Nothing
	// was changed and the proposal is stale.
	ErrPullHeadChanged = errors.New("the branch tip is no longer the one that was proposed")
	// ErrPullBusy: the main checkout is in use (the shared main lock is held).
	ErrPullBusy = errors.New("the main checkout is in use")
)

// execPull fast-forwards the main checkout after a confirmation (AC-04). The
// PullRunner is the existing git-action service and takes the shared main lock
// itself, so this executor does not hold it (it would block the runner). There
// is no merge, rebase or reset fallback: a refusal changes nothing. A nil error
// from the runner means the pull was done or the branch was already up to date;
// every refusal (dirty, diverged, no upstream, detached) is an error with a
// user message.
func (s *Service) execPull(ctx context.Context, p coreprop.Proposal, a coreprop.PullFF, attempt int) outcome {
	if s.cfg.Pull == nil {
		return failed(coreprop.FailPrecondition, false, "Pulling is not set up in this build of Yggdrasil, so nothing was changed.")
	}
	if err := a.Validate(); err != nil {
		return failed(coreprop.FailPrecondition, false, "This pull proposal is not valid any more, so nothing was changed. Ask for a new one.")
	}
	wd, stop := s.open(ctx, p, coreprop.MainCheckout{Repo: a.Repo}, false)
	if stop != nil {
		return *stop
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()

	branch, err := s.cfg.Git.(BranchReader).CurrentBranch(ctx, wd.dir)
	if err != nil {
		return readFailure("check the checked-out branch", err)
	}
	head, err := s.cfg.Git.Head(ctx, wd.dir)
	if err != nil {
		return readFailure("read the branch tip", err)
	}
	switch coreprop.PlanPull(a, branch, head) {
	case coreprop.PlanPullStale:
		s.auditStageBestEffort(ctx, p, attempt, stagePullRefused)
		return pullStale()
	case coreprop.PlanPullWrongBranch:
		s.auditStageBestEffort(ctx, p, attempt, stagePullRefused)
		return failed(coreprop.FailPrecondition, false, "Another branch is checked out in the main checkout now, so nothing was pulled. Ask for a new proposal.")
	}
	if err := s.auditStage(ctx, p, attempt, stagePullStarted); err != nil {
		return failed(coreprop.FailExec, true, "Yggdrasil could not record that it is about to pull, so it did nothing. Try again.")
	}
	res, err := s.cfg.Pull.PullFF(ctx, a.Repo, a.Branch, a.Upstream, a.ExpectHead, a.Verify)
	if err != nil {
		out := pullFailure(err)
		stage := stagePullRefused
		if out.kind == coreprop.FailInterrupted {
			stage = stagePullUnsure
		}
		s.auditStageBestEffort(ctx, p, attempt, stage)
		return out
	}
	s.auditStageBestEffort(ctx, p, attempt, stagePullDone)
	return done(pullResult(a, res))
}

func pullStale() outcome {
	return failed(coreprop.FailStale, false, "The branch has moved since this pull was proposed, so nothing was pulled. Reject this and ask for a new proposal.")
}

func pullResult(a coreprop.PullFF, res PullFFResult) coreprop.Result {
	note := res.Note
	if note == "" {
		if res.NewHead == "" {
			note = "Already up to date. Nothing was changed."
		} else {
			note = "Fast-forwarded " + a.Branch + " from " + a.Upstream + "."
		}
	}
	r := coreprop.Result{CommitID: res.NewHead, BuildID: res.BuildID, Note: note}
	if a.Verify && res.BuildID == "" {
		r.Note += " The verification checks were not started."
	}
	return r
}

func pullFailure(err error) outcome {
	switch {
	case errors.Is(err, ErrPullHeadChanged):
		return pullStale()
	case errors.Is(err, ErrPullBusy):
		return failed(coreprop.FailPrecondition, true, "The main checkout of this repo is in use. Nothing was pulled. Retry when it has finished.")
	case isContextEnd(err):
		return failed(coreprop.FailInterrupted, true, "The pull was stopped before it finished. Check the repo, then retry if needed.")
	}
	var um userMessager
	if errors.As(err, &um) && um.UserMessage() != "" {
		// A refusal of the fast-forward (dirty, diverged, no upstream): nothing
		// changed; retry after fixing it, with the same proposal.
		return failed(coreprop.FailPrecondition, true, "Nothing was pulled. %s", um.UserMessage())
	}
	return failed(coreprop.FailExec, true, "The pull did not finish. Check the repo, then retry if needed.")
}

// reconcilePull judges, read-only, a crashed pull: the tip is still the
// expected one means nothing was pulled; any other tip is not claimed as ours.
func (s *Service) reconcilePull(ctx context.Context, p coreprop.Proposal, a coreprop.PullFF) (coreprop.ReconcileOutcome, coreprop.Result) {
	wd, stop := s.open(ctx, p, coreprop.MainCheckout{Repo: a.Repo}, false)
	if stop != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	head, err := s.cfg.Git.Head(ctx, wd.dir)
	if err != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	return coreprop.ReconcilePull(a, head), coreprop.Result{}
}
