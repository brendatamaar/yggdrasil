package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// ErrOutcomeUnknown is wrapped by an error from CommentHost.PostReviewComment
// when the request may or may not have reached the host (a timeout, a lost or
// unreadable reply, a 3xx/5xx/429). The wiring adapter around hosts.Service
// wraps hosts.ErrOutcomeUnknown with it (internal/hosts may not be imported
// here). The executor does not depend on it for safety: every error that is not
// proven to be a refusal is treated as unknown, so an adapter that forgets to
// wrap cannot cause a second POST.
var ErrOutcomeUnknown = errors.New("the host did not say whether the comment was posted")

// ErrWriteRefused is wrapped by an error from PostReviewComment when the host
// looked at the request and refused it, so nothing was created (a 400/422
// rejection, a denied token, or a refusal before any request was sent). Only
// such an error is a definite failure; there is nothing to reconcile.
var ErrWriteRefused = errors.New("the host refused the comment and nothing was posted")

// nothingWritten is the method form of ErrWriteRefused, for errors that cannot
// wrap this package's sentinel.
type nothingWritten interface{ NothingWritten() bool }

func definiteRefusal(err error) bool {
	if errors.Is(err, ErrWriteRefused) {
		return true
	}
	var nw nothingWritten
	return errors.As(err, &nw) && nw.NothingWritten() && !errors.Is(err, ErrOutcomeUnknown)
}

const commentUnknownMsg = "Yggdrasil could not tell whether the comment was posted, and it will not post it again on its own. Look at the merge request for a comment ending with the marker shown in this proposal. Retry checks the host again first, and posts only if the comment is certainly not there."

// execComment posts one confirmed review comment (AC-02). It rechecks that the
// repo is tracked and that the MR still points at the reviewed head, records a
// durable effect (with the marker) BEFORE the single POST, never retries a POST
// whose outcome is unknown, and reconciles a lost reply by reading the host's
// comments for the marker.
func (s *Service) execComment(ctx context.Context, p coreprop.Proposal, a coreprop.ReviewComment, attempt int) outcome {
	if s.cfg.Comments == nil {
		return failed(coreprop.FailPrecondition, false, "Posting comments is not set up in this build of Yggdrasil, so nothing was posted.")
	}
	if err := a.Validate(); err != nil {
		return failed(coreprop.FailPrecondition, false, "This comment proposal is not valid any more, so nothing was posted. Ask for a new one.")
	}
	if !s.repoTracked(ctx, a.Repo) {
		return failed(coreprop.FailRepoUntracked, false, "This repo is no longer tracked, so nothing was posted. Track the repo again, then ask for a new proposal.")
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()

	effects, err := s.cfg.Store.ListEffectsByProposal(ctx, p.ID)
	if err != nil {
		return failed(coreprop.FailExec, true, "Yggdrasil could not read its record of this comment, so it posted nothing. Try again.")
	}
	var states []coreprop.EffectStatus
	for _, e := range effects {
		if e.Attempt == attempt {
			// This attempt already started once: never send twice in one attempt.
			return failed(coreprop.FailInterrupted, true, "An earlier run of this attempt did not finish. Nothing was posted again. Retry checks the host first.")
		}
		states = append(states, coreprop.EffectStatus(e.State))
	}
	prior := coreprop.PriorOfEffects(states)

	head := ""
	if prior == coreprop.PriorNone || prior == coreprop.PriorNotPosted {
		if head, err = s.cfg.Comments.MRHead(ctx, a.Repo, a.MRNumber); err != nil {
			return commentReadFailure(err)
		}
	}
	switch coreprop.PlanComment(a, prior, head) {
	case coreprop.PlanCommentDone:
		return s.commentAlreadyPosted(effects)
	case coreprop.PlanCommentStale:
		return failed(coreprop.FailStale, false, "The merge request has new commits since this comment was written, so nothing was posted. Reject this and ask for a new review.")
	case coreprop.PlanCommentLookup:
		out, posted := s.lookupAndSettle(ctx, p, a, effects)
		if posted {
			return out
		}
		if out.kind != "" {
			return out
		}
		// Certainly absent and a human retried: the head is checked again, then
		// exactly one POST.
		if head, err = s.cfg.Comments.MRHead(ctx, a.Repo, a.MRNumber); err != nil {
			return commentReadFailure(err)
		}
		if coreprop.PlanComment(a, coreprop.PriorNotPosted, head) != coreprop.PlanCommentPost {
			return failed(coreprop.FailStale, false, "The merge request has new commits since this comment was written, so nothing was posted. Reject this and ask for a new review.")
		}
	}
	return s.postComment(ctx, p, a, attempt)
}

func commentReadFailure(err error) outcome {
	if isContextEnd(err) {
		return failed(coreprop.FailInterrupted, true, "Reading the merge request took too long and was stopped. Nothing was posted. Retry when the host answers.")
	}
	return failed(coreprop.FailExec, true, "%s", userText(err, "Yggdrasil could not read the merge request from the host, so nothing was posted. Check the connection and the host token, then retry."))
}

// commentAlreadyPosted answers for an earlier attempt whose effect recorded the
// comment: nothing is sent.
func (s *Service) commentAlreadyPosted(effects []db.EffectRecord) outcome {
	for _, e := range effects {
		if e.State == db.EffectSucceeded {
			return done(coreprop.Result{Note: "The comment was already posted (comment " + e.RemoteID + "). Nothing was posted again."})
		}
	}
	return failed(coreprop.FailInterrupted, true, "Yggdrasil could not find its record of the posted comment. Look at the merge request, then retry if needed.")
}

// lookupAndSettle asks the host, read-only, whether the comment of an earlier,
// unsettled attempt exists. posted is true when it does (the effect is settled
// succeeded and out is the executed result). Otherwise a zero out (kind "")
// means certainly absent (the earlier effects stay unsettled, so every later
// attempt asks again before it sends); a non-zero out blocks any POST.
func (s *Service) lookupAndSettle(ctx context.Context, p coreprop.Proposal, a coreprop.ReviewComment, effects []db.EffectRecord) (out outcome, posted bool) {
	open := openEffects(effects)
	l, err := s.cfg.Comments.FindReviewComment(ctx, a.Repo, a.MRNumber, a.Marker, coreprop.CommentBodyHash(a.Body), a.ExpectedHead)
	if err != nil {
		if isContextEnd(err) {
			return failed(coreprop.FailInterrupted, true, "Checking the merge request for the comment took too long. Nothing was posted. Retry when the host answers."), false
		}
		return failed(coreprop.FailInterrupted, true, "%s", commentUnknownMsg), false
	}
	verdict, receipt := coreprop.JudgeCommentLookup(a, l)
	switch verdict {
	case coreprop.CommentPosted:
		if err := s.recordPosted(ctx, p, open, receipt); err != nil {
			s.cfg.Logger.Error("could not record a found comment", "proposal", p.ID, "error", s.redact(err.Error()))
		}
		return done(coreprop.Result{Note: "The comment is on the merge request (comment " + receipt.CommentID + "). It was found, not posted again."}), true
	case coreprop.CommentMayRetry:
		// The earlier effects stay unsettled on purpose: the next attempt asks
		// the host again before it sends anything.
		s.auditStageBestEffort(ctx, p, lastAttempt(open), stageCommentAbsent)
		return outcome{}, false
	}
	for _, e := range open {
		if e.State != db.EffectUnknown {
			_ = s.settleEffect(ctx, p, e, db.EffectUnknown, "", nil, "", stageCommentUnknown)
		}
	}
	return failed(coreprop.FailInterrupted, true, "%s", commentUnknownMsg), false
}

func lastAttempt(effects []db.EffectRecord) int {
	n := 1
	for _, e := range effects {
		n = max(n, e.Attempt)
	}
	return n
}

// openEffects are the effects that started and never settled to a final state.
func openEffects(effects []db.EffectRecord) []db.EffectRecord {
	var out []db.EffectRecord
	for _, e := range effects {
		if e.State == db.EffectIntent || e.State == db.EffectUnknown {
			out = append(out, e)
		}
	}
	return out
}

// recordPosted settles the earliest open effect as succeeded with the receipt
// and the rest as failed (they sent nothing the host kept).
func (s *Service) recordPosted(ctx context.Context, p coreprop.Proposal, open []db.EffectRecord, r coreprop.CommentReceipt) error {
	kind, raw, err := db.EncodeCommentReceipt(r)
	if err != nil {
		return err
	}
	if len(open) == 0 {
		return nil
	}
	slices.SortFunc(open, func(x, y db.EffectRecord) int { return y.Attempt - x.Attempt }) // newest first
	if err := s.settleEffect(ctx, p, open[0], db.EffectSucceeded, kind, json.RawMessage(raw), r.CommentID, stageCommentRecovered); err != nil {
		return err
	}
	for _, e := range open[1:] {
		_ = s.settleEffect(ctx, p, e, db.EffectFailed, "", nil, "", stageCommentAbsent)
	}
	return nil
}

// postComment records the intent, sends exactly one POST and settles the
// effect. A lost reply is reconciled once, read-only.
func (s *Service) postComment(ctx context.Context, p coreprop.Proposal, a coreprop.ReviewComment, attempt int) outcome {
	if err := s.beginEffect(ctx, p, attempt, a.Marker, stageCommentStarted); err != nil {
		if errors.Is(err, errEffectExists) {
			return failed(coreprop.FailInterrupted, true, "An earlier run of this attempt did not finish. Nothing was posted again. Retry checks the host first.")
		}
		return failed(coreprop.FailExec, true, "Yggdrasil could not record that it is about to post, so it posted nothing. Try again.")
	}
	eff, err := s.cfg.Store.GetEffectByKey(ctx, p.ID, "", attempt)
	if err != nil {
		return failed(coreprop.FailInterrupted, true, "Yggdrasil lost its record of this comment before posting. Nothing was posted. Retry checks the host first.")
	}
	id, err := s.cfg.Comments.PostReviewComment(ctx, a.Repo, a.MRNumber, a.ExpectedHead, a.Body, a.Marker)
	if err == nil {
		receipt := coreprop.CommentReceipt{CommentID: id, Marker: a.Marker, BodyHash: coreprop.CommentBodyHash(a.Body), Head: a.ExpectedHead}
		kind, raw, encErr := db.EncodeCommentReceipt(receipt)
		if encErr == nil {
			if serr := s.settleEffect(ctx, p, eff, db.EffectSucceeded, kind, json.RawMessage(raw), id, stageCommentPosted); serr == nil {
				return done(coreprop.Result{Note: "Posted the comment on the merge request (comment " + id + ")."})
			}
			// Posted, but the record could not be written: the effect stays
			// started, and the next attempt finds the comment by its marker.
			return failed(coreprop.FailInterrupted, true, "The comment was posted, but Yggdrasil could not record it. Retry will find it on the merge request and finish without posting again.")
		}
		// The host answered with an id this code cannot trust: reconcile by marker.
		err = ErrOutcomeUnknown
	}
	if definiteRefusal(err) {
		if serr := s.settleEffect(ctx, p, eff, db.EffectFailed, "", nil, "", stageCommentRejected); serr != nil {
			s.cfg.Logger.Error("could not record a refused comment", "proposal", p.ID, "error", s.redact(serr.Error()))
		}
		return failed(coreprop.FailRemote, true, "%s", userText(err, "The host refused the comment and nothing was posted. Check the merge request and the host token, then retry."))
	}
	// Unknown: never resend. Mark it, then look once, read-only.
	if serr := s.settleEffect(ctx, p, eff, db.EffectUnknown, "", nil, "", stageCommentUnknown); serr != nil {
		s.cfg.Logger.Error("could not record an unknown comment outcome", "proposal", p.ID, "error", s.redact(serr.Error()))
	}
	if ctx.Err() != nil {
		return failed(coreprop.FailInterrupted, true, "%s", commentUnknownMsg)
	}
	eff.State = db.EffectUnknown
	out, posted := s.lookupAndSettle(ctx, p, a, []db.EffectRecord{eff})
	if posted {
		return out
	}
	if out.kind == "" {
		// Certainly absent: the POST did not land. A human decides to retry.
		return failed(coreprop.FailInterrupted, true, "The host did not confirm the comment, and it is not on the merge request. It was not posted again. Retry to post it once more.")
	}
	return out
}

// reconcileComment judges, read-only, a crashed comment execution: the local
// effect record first, then the host's comments for the marker.
func (s *Service) reconcileComment(ctx context.Context, p coreprop.Proposal, a coreprop.ReviewComment) (coreprop.ReconcileOutcome, coreprop.Result) {
	unknown := func() (coreprop.ReconcileOutcome, coreprop.Result) {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	if s.cfg.Comments == nil || !s.repoTracked(ctx, a.Repo) {
		return unknown()
	}
	effects, err := s.cfg.Store.ListEffectsByProposal(ctx, p.ID)
	if err != nil {
		return unknown()
	}
	for _, e := range effects {
		if e.State == db.EffectSucceeded {
			return coreprop.ReconcileDone, coreprop.Result{Note: "The comment was posted (comment " + e.RemoteID + ")."}
		}
	}
	open := openEffects(effects)
	if len(open) == 0 {
		// No attempt ever reached the host: nothing was sent.
		return coreprop.ReconcileNotDone, coreprop.Result{}
	}
	out, posted := s.lookupAndSettle(ctx, p, a, effects)
	switch {
	case posted:
		return coreprop.ReconcileDone, out.result
	case out.kind == "":
		return coreprop.ReconcileNotDone, coreprop.Result{}
	}
	return unknown()
}
