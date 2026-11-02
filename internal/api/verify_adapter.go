package api

import (
	"context"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	vsvc "github.com/brendatamaar/yggdrasil/internal/verify"
)

// The adapter from *verify.Service to VerificationService. It maps the
// service's Records to DTOs; the pure Summarize decides the task summary. The
// file is Go-only wiring (excluded from tygo).

// VerifyBackend is what the adapter needs of *verify.Service.
type VerifyBackend interface {
	List(ctx context.Context, task core.TaskID) ([]vsvc.Record, error)
	Retry(ctx context.Context, task core.TaskID) ([]vsvc.Record, error)
	Get(ctx context.Context, id cv.VerificationID) (vsvc.Record, error)
	Cancel(ctx context.Context, id cv.VerificationID) (vsvc.Record, error)
	SendFailure(ctx context.Context, id cv.VerificationID) error
}

// FailureDetailer is optionally implemented by the backend. It returns the
// name of the failed step and the REDACTED tail of its log (the same text
// SendFailure sends to the agent), or "" for either when unknown. The adapter
// never reads a log itself, so unredacted log text has no other way out.
type FailureDetailer interface {
	FailureDetail(ctx context.Context, rec vsvc.Record) (step, tail string)
}

// RepoNamer returns a repo's display name, or "" when unknown.
type RepoNamer func(ctx context.Context, repo core.RepoID) string

type verificationAdapter struct {
	b     VerifyBackend
	names RepoNamer
}

// NewVerificationService adapts the verification service. names may be nil.
func NewVerificationService(b VerifyBackend, names RepoNamer) VerificationService {
	return &verificationAdapter{b: b, names: names}
}

func (a *verificationAdapter) List(ctx context.Context, task core.TaskID) (VerificationsResponse, error) {
	recs, err := a.b.List(ctx, task)
	if err != nil {
		return VerificationsResponse{}, err
	}
	return a.response(ctx, recs), nil
}

func (a *verificationAdapter) Retry(ctx context.Context, task core.TaskID) (VerificationsResponse, error) {
	recs, err := a.b.Retry(ctx, task)
	if err != nil {
		return VerificationsResponse{}, err
	}
	return a.response(ctx, recs), nil
}

func (a *verificationAdapter) Cancel(ctx context.Context, id cv.VerificationID) (VerificationDTO, error) {
	rec, err := a.b.Cancel(ctx, id)
	if err != nil {
		return VerificationDTO{}, err
	}
	return a.dto(ctx, rec), nil
}

func (a *verificationAdapter) SendFailure(ctx context.Context, id cv.VerificationID) (SendFailureResponse, error) {
	// The service apperr errors (busy, already sent, result unknown) pass
	// through unchanged: they are Conflicts with plain-language messages.
	rec, err := a.b.Get(ctx, id)
	if err != nil {
		return SendFailureResponse{}, err
	}
	if err := a.b.SendFailure(ctx, id); err != nil {
		return SendFailureResponse{}, err
	}
	// The service does not report the new turn; the client refetches the task.
	return SendFailureResponse{TaskID: rec.Task}, nil
}

func (a *verificationAdapter) response(ctx context.Context, recs []vsvc.Record) VerificationsResponse {
	out := VerificationsResponse{Verifications: make([]VerificationDTO, 0, len(recs))}
	states := make([]cv.State, 0, len(recs))
	for _, r := range recs {
		out.Verifications = append(out.Verifications, a.dto(ctx, r))
		states = append(states, r.State)
	}
	out.Summary = VerificationSummary(states)
	return out
}

func (a *verificationAdapter) dto(ctx context.Context, rec vsvc.Record) VerificationDTO {
	var name string
	if a.names != nil {
		name = a.names(ctx, rec.Repo)
	}
	var step, tail string
	if f, ok := a.b.(FailureDetailer); ok {
		if _, failed := rec.State.(cv.Failed); failed {
			step, tail = f.FailureDetail(ctx, rec)
		}
	}
	return VerificationFromRecord(rec, name, step, tail)
}

// VerificationFromRecord maps one record. step and tail come from the
// service's FailureDetail (redacted); when empty the step is "step N" and the
// message is the plain failure text of a step that could not start.
func VerificationFromRecord(rec vsvc.Record, repoName, step, tail string) VerificationDTO {
	d := VerificationDTO{
		ID: string(rec.ID), TaskID: rec.Task, RepoID: rec.Repo, RepoName: repoName, WorktreeID: string(rec.Worktree),
		State: VerifyNone,
	}
	utc := func(t time.Time) *time.Time { t = t.UTC(); return &t }
	switch st := rec.State.(type) {
	case cv.Skipped:
		d.State, d.Turn = VerifySkipped, string(st.Turn)
		switch st.Reason {
		case cv.SkipNoChanges:
			d.Reason = "The agent has not changed anything in this repo yet."
		case cv.SkipDisabled:
			d.Reason = "Automatic verification is switched off for this project."
		}
	case cv.Unverifiable:
		d.State, d.Turn, d.Attempt = VerifyUnverifiable, string(st.Turn), int(st.Attempt)
		switch st.Reason {
		case cv.UnverifiableNoCommand:
			d.Reason = "This repo has no verify command. Set one in the repo's build settings."
		case cv.UnverifiableNotTrusted:
			d.Reason = "This repo is not trusted, so Yggdrasil will not run its commands. Trust it first."
		case cv.UnverifiableWorktreeMissing:
			d.Reason = "The task's worktree for this repo is missing."
		case cv.UnverifiableCouldNotQueue:
			d.Reason = "Yggdrasil could not queue the checks. Verify again in a moment."
		}
		if rec.Note != "" {
			d.Reason += " " + rec.Note
		}
	case cv.Bootstrapping:
		d.State, d.Turn, d.Attempt, d.BuildID, d.Since = VerifyBootstrapping, string(st.Turn), int(st.Attempt), st.Build, utc(st.Since)
		d.Stage = string(cv.StageBootstrap)
	case cv.Verifying:
		d.State, d.Turn, d.Attempt, d.BuildID, d.Since = VerifyVerifying, string(st.Turn), int(st.Attempt), st.Build, utc(st.Since)
		d.Stage = string(cv.StageVerify)
	case cv.Passed:
		d.State, d.Turn, d.Attempt, d.BuildID, d.At = VerifyPassed, string(st.Turn), int(st.Attempt), st.Build, utc(st.At)
	case cv.Failed:
		d.State, d.Turn, d.Attempt, d.BuildID, d.At = VerifyFailed, string(st.Turn), int(st.Attempt), st.Build, utc(st.At)
		d.Stage = string(st.Stage)
		d.FailedStep = step
		if d.FailedStep == "" {
			d.FailedStep = fmt.Sprintf("step %d", st.Step+1)
		}
		switch r := st.Reason.(type) {
		case cv.ExitCode:
			code := r.Code
			d.ExitCode = &code
			d.Message = tail
			d.Reason = fmt.Sprintf("%s exited with code %d.", d.FailedStep, code)
		case cv.StartFailed:
			d.Message = string(r.Failure)
			d.Reason = d.FailedStep + " could not start."
		}
		d.CanSendFailure = !rec.Sent && !rec.Sending
	case cv.Interrupted:
		d.State, d.Turn, d.Attempt, d.At = VerifyInterrupted, string(st.Turn), int(st.Attempt), utc(st.At)
		d.Stage = string(st.Stage)
		d.Reason = "Yggdrasil stopped while this was running. Verify again."
	case cv.Cancelled:
		d.State, d.Turn, d.Attempt, d.At = VerifyCancelled, string(st.Turn), int(st.Attempt), utc(st.At)
		d.CancelCause = string(st.Cause)
		switch st.Cause {
		case cv.CancelNotTrusted:
			d.Reason = "Stopped: this repo is no longer trusted."
		case cv.CancelUser:
			d.Reason = "Stopped by you."
		default:
			d.Reason = "Stopped before it finished."
		}
	}
	return d
}

// VerificationSummary derives the task summary from the repo states with the
// core's Summarize. It is never stored.
func VerificationSummary(states []cv.State) VerificationSummaryDTO {
	out := VerificationSummaryDTO{Total: len(states)}
	for _, s := range states {
		switch s.(type) {
		case cv.Passed:
			out.Passed++
		case cv.Failed:
			out.Failed++
		case cv.Bootstrapping, cv.Verifying:
			out.Running++
		}
	}
	switch cv.Summarize(states) {
	case cv.SummarySkipped:
		out.State = VerifySkipped
	case cv.SummaryPassed:
		out.State = VerifyPassed
	case cv.SummaryRunning:
		out.State = VerifyVerifying
	case cv.SummaryCancelled:
		out.State = VerifyCancelled
	case cv.SummaryUnverifiable:
		out.State = VerifyUnverifiable
	case cv.SummaryInterrupted:
		out.State = VerifyInterrupted
	case cv.SummaryFailed:
		out.State = VerifyFailed
	default:
		out.State = VerifyNone
	}
	return out
}

// VerificationSummarizer is optionally implemented by the VerificationService
// (the adapter from NewVerificationService does). The board and the task view
// use it to show the worst verification state without reading any log.
type VerificationSummarizer interface {
	// Summary returns the summary of one task; ok is false when it has none
	// (no verification rows yet).
	Summary(ctx context.Context, task core.TaskID) (sum VerificationSummaryDTO, ok bool, err error)
}

func (a *verificationAdapter) Summary(ctx context.Context, task core.TaskID) (VerificationSummaryDTO, bool, error) {
	recs, err := a.b.List(ctx, task)
	if err != nil {
		return VerificationSummaryDTO{}, false, err
	}
	if len(recs) == 0 {
		return VerificationSummaryDTO{}, false, nil
	}
	states := make([]cv.State, 0, len(recs))
	for _, r := range recs {
		states = append(states, r.State)
	}
	return VerificationSummary(states), true, nil
}

// verificationSummary returns the task's summary for the board and the task
// view, or nil when verification is not wired, the task has none or reading
// it failed (the board never fails because of it; the error is logged).
func (s *Server) verificationSummary(ctx context.Context, task core.TaskID) *VerificationSummaryDTO {
	sm, ok := s.phase4().Verification.(VerificationSummarizer)
	if !ok {
		return nil
	}
	sum, has, err := sm.Summary(ctx, task)
	if err != nil {
		s.log.Warn("could not read the verification summary", "task", task, "err", err)
		return nil
	}
	if !has {
		return nil
	}
	return &sum
}
