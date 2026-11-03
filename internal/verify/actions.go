package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// readInputs reads the facts and the plan for a record at the boundary. It
// reads only what the next rule needs: git runs only for an enabled, trusted
// repo with an active worktree. An error (database, git) is the caller's to
// retry; nothing is stored.
func (s *Service) readInputs(ctx context.Context, rec Record) (cv.Facts, cv.Plan, string, error) {
	var facts cv.Facts
	enabled, err := s.d.Store.AutoVerify(ctx)
	if err != nil {
		return facts, cv.Plan{}, "", fmt.Errorf("read verification setting: %w", err)
	}
	facts.Enabled = enabled
	if !enabled {
		return facts, cv.Plan{}, "", nil
	}
	wts, err := s.d.Store.Worktrees(ctx, rec.Task)
	if err != nil {
		return facts, cv.Plan{}, "", fmt.Errorf("list worktrees of %s: %w", rec.Task, err)
	}
	var dir string
	for _, w := range wts {
		if w.ID == rec.Worktree {
			facts.WorktreeActive, dir = true, w.Path
		}
	}
	if !facts.WorktreeActive {
		return facts, cv.Plan{}, "", nil
	}
	trusted, err := s.repoTrusted(ctx, rec.Repo)
	if err != nil {
		return facts, cv.Plan{}, "", fmt.Errorf("read trust of %s: %w", rec.Repo, err)
	}
	facts.Trusted = trusted
	if !trusted {
		return facts, cv.Plan{}, "", nil
	}
	base, err := s.d.Store.BaseCommit(ctx, rec.Worktree)
	if err != nil {
		return facts, cv.Plan{}, "", fmt.Errorf("read base commit of %s: %w", rec.Worktree, err)
	}
	has, err := s.d.Probe.HasChanges(ctx, dir, base)
	if err != nil {
		return facts, cv.Plan{}, "", fmt.Errorf("check changes of %s: %w", rec.Worktree, err)
	}
	facts.HasChanges = has
	if !has {
		return facts, cv.Plan{}, "", nil
	}
	srec, err := s.d.Store.BuildSettings(ctx, rec.Repo)
	if err != nil {
		return facts, cv.Plan{}, "", fmt.Errorf("read build settings of %s: %w", rec.Repo, err)
	}
	settings, perr := ParseSettings(srec)
	if perr != nil {
		// A domain outcome, not a retry: no verify chain, with a plain note.
		return facts, cv.Plan{}, NoteBadSettings, nil //nolint:nilerr // invalid settings are a domain outcome, not a retryable error
	}
	plan, note := ResolvePlan(s.d.OpenFS(dir), s.d.Toolchains, settings)
	return facts, plan, note, nil
}

// List returns the stored verifications of a task. A row that cannot be read
// is logged and left out.
func (s *Service) List(ctx context.Context, task core.TaskID) ([]Record, error) {
	rows, err := s.d.Store.ListByTask(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("list verifications of %s: %w", task, err)
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		rec, perr := VerificationFromRow(row)
		if perr != nil {
			s.d.Log.Error("a verification is stored in a form that cannot be read", "verification", row.ID, "err", perr)
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// Get returns one verification, or an apperr NotFound.
func (s *Service) Get(ctx context.Context, id cv.VerificationID) (Record, error) {
	row, err := s.d.Store.Get(ctx, string(id))
	if err != nil {
		if isNotFound(err) {
			return Record{}, apperr.New(apperr.NotFound, "That verification doesn't exist.", err)
		}
		return Record{}, fmt.Errorf("get verification %s: %w", id, err)
	}
	return VerificationFromRow(row)
}

// Retry is "Verify again": every repo of the task whose verification is not
// running starts a new attempt. A running one is left alone (the user cancels
// first). It returns the verifications as they are afterwards.
func (s *Service) Retry(ctx context.Context, task core.TaskID) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.List(ctx, task)
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		switch rec.State.(type) {
		case cv.Bootstrapping, cv.Verifying, cv.None:
			continue
		}
		_, _, rerr := s.run(ctx, keyOf(rec), userActor, func(r Record) (cv.Input, string, bool, error) {
			facts, plan, note, ferr := s.readInputs(ctx, r)
			if ferr != nil {
				return nil, "", false, ferr
			}
			return cv.Retry{
				Facts: facts, Plan: plan, BootBuild: core.BuildID(s.d.IDs(buildIDPrefix)), VerifyBuild: core.BuildID(s.d.IDs(buildIDPrefix)), Now: s.now(),
			}, note, true, nil
		})
		if rerr != nil {
			return nil, apperr.New(apperr.Unavailable, "Yggdrasil couldn't start the checks again. Try again in a moment.", rerr)
		}
	}
	return s.List(ctx, task)
}

// Cancel stops a live verification. A verification that is not running is a
// conflict.
func (s *Service) Cancel(ctx context.Context, id cv.VerificationID) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if _, _, live := liveOf(rec.State); !live {
		return Record{}, apperr.New(apperr.Conflict, "Nothing is running for this repo.", errors.New("verification is not live"))
	}
	after, changed, err := s.run(ctx, keyOf(rec), userActor, func(Record) (cv.Input, string, bool, error) {
		return cv.Cancel{Now: s.now()}, "", true, nil
	})
	if err != nil {
		return Record{}, apperr.New(apperr.Unavailable, "Yggdrasil couldn't cancel the checks. Try again.", err)
	}
	if !changed {
		return Record{}, apperr.New(apperr.Conflict, "The checks already finished.", errors.New("verification is not live"))
	}
	return after, nil
}

// SendFailure is "Send failure to agent" (VF-03): it composes the message with
// the pure ComposeFailure from the failed step and the redacted tail of its
// log, and hands it to the task as a new message. It is not an input of Step:
// the tasks service owns the new turn and its audit. A busy task is the
// messenger's refusal, passed on unchanged.
//
// A failed attempt is sent once: the send is recorded in the verification's
// row, together with its audit entry, so a second call (a double click, two
// requests at once) is a conflict and sends nothing. A new attempt is a new
// state and can be sent again.
func (s *Service) SendFailure(ctx context.Context, id cv.VerificationID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	failed, ok := rec.State.(cv.Failed)
	if !ok {
		return apperr.New(apperr.Conflict, "Only a failed check can be sent to the agent.", errors.New("verification has not failed"))
	}
	if rec.Sent || rec.Sending {
		return apperr.New(apperr.Conflict, "This failure was already sent to the agent. Verify again to send a new one.", errors.New("failure already sent for this attempt"))
	}
	report := cv.FailureReport{
		Repo: rec.Repo, Stage: failed.Stage, Step: fmt.Sprintf("step %d", failed.Step+1), Reason: failed.Reason,
	}
	report.Step, report.LogTail = s.failureParts(ctx, failed, report.Step)
	text, err := cv.ComposeFailure(report)
	if err != nil {
		return apperr.New(apperr.Invalid, "Yggdrasil couldn't write the message for the agent.", err)
	}
	// Redact the whole message, step name included, not only the log tail.
	text = s.d.Redactor.Redact(text)

	// Mark the attempt as being sent, durably, before the message leaves: a
	// crash can lose a send but never repeat one.
	if err := s.markSend(ctx, rec, failed, sendMark{Sending: true}, "verify.send_failure_begin"); err != nil {
		return apperr.New(apperr.Unavailable, "Yggdrasil couldn't prepare to send the failure. Try again.", err)
	}
	sendErr := s.sendMessage(ctx, rec.Task, text)
	cur, gerr := s.Get(ctx, rec.ID)
	if gerr != nil {
		s.d.Log.Error("could not reload a verification after sending a failure", "verification", rec.ID, "err", gerr)
		if sendErr != nil && !isKnownRefusal(sendErr) {
			return unknownSendError()
		}
		return sendErr
	}
	switch {
	case sendErr == nil:
		if err := s.markSend(ctx, cur, failed, sendMark{Sent: true}, "verify.send_failure"); err != nil {
			s.d.Log.Error("could not record that a failure was sent; it stays marked as sending", "verification", rec.ID, "err", err)
		}
		return nil
	case isKnownRefusal(sendErr):
		if err := s.markSend(ctx, cur, failed, sendMark{}, "verify.send_failure_refused"); err != nil {
			s.d.Log.Error("could not clear the sending mark after a refusal; it stays marked as sending", "verification", rec.ID, "err", err)
		}
		return sendErr
	default:
		// Unknown outcome: the agent may have received it. Stay marked.
		s.d.Log.Error("result of sending a failure is unknown; not sending again", "verification", rec.ID, "err", sendErr)
		return unknownSendError()
	}
}

// isKnownRefusal reports whether the messenger said, for certain, that it did
// not take the message: a classified error other than Unavailable (a busy
// task is a conflict). Anything else (timeout, panic, plain error) is unknown.
func isKnownRefusal(err error) bool {
	c, ok := apperr.ClassOf(err)
	return ok && c != apperr.Unavailable
}

func unknownSendError() error {
	return apperr.New(apperr.Conflict, "Yggdrasil couldn't tell whether the agent received the failure. Check the agent before sending it again, or verify again to start a new attempt.", errors.New("send result unknown"))
}

func (s *Service) sendMessage(ctx context.Context, task core.TaskID, text string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sending a failure panicked: %v", r)
		}
	}()
	return s.d.Messenger.SendMessage(ctx, task, text)
}

// markSend stores the send mark on the row and the audit entry in one
// transaction.
func (s *Service) markSend(ctx context.Context, rec Record, failed cv.Failed, mark sendMark, action string) error {
	now := s.now()
	detail, err := json.Marshal(map[string]string{"task": string(rec.Task), "repo": string(rec.Repo), "turn": string(failed.Turn), "outcome": string(failed.Stage)})
	if err != nil {
		return fmt.Errorf("audit detail: %w", err)
	}
	e := core.AuditEntry{Actor: userActor, Action: action, Subject: "verification:" + string(rec.ID), Detail: detail}
	row, err := rowFromVerification(rec.Verification, rec.Note, mark)
	if err != nil {
		return err
	}
	return s.d.Store.Commit(ctx, func(tx Tx) error {
		cur, lerr := tx.LoadByTaskRepo(ctx, rec.Task, rec.Repo)
		if lerr != nil {
			return lerr
		}
		if cur.Version != rec.Version {
			return fmt.Errorf("verification %s moved to version %d: %w", rec.ID, cur.Version, db.ErrConflict)
		}
		pe, perr := audit.Prepare(e, s.d.Redactor, core.AuditID(s.d.IDs("aud")), now)
		if perr != nil {
			return perr
		}
		if _, serr := tx.Save(ctx, row, rec.Version, now); serr != nil {
			return serr
		}
		return tx.AppendAudit(ctx, pe)
	})
}

// redactedTail redacts the whole bounded read first, then drops the start of a
// cut log, then keeps the last MaxFailureLogBytes. The start of a cut log may
// hold the end of a secret whose beginning was dropped, which the redactor
// cannot recognize. It lies before any match, so it is at the same place in
// the redacted text: the partial first line is dropped (or, in a log without a
// line break, the first logCutMargin bytes).
func (s *Service) redactedTail(raw []byte, cut bool) string {
	text := s.d.Redactor.Redact(strings.ToValidUTF8(string(raw), "�"))
	if cut {
		head := text[:min(len(text), logCutMargin*8)]
		if nl := strings.IndexByte(head, '\n'); nl >= 0 {
			text = text[nl+1:]
		} else {
			text = text[min(len(text), logCutMargin):]
		}
	}
	return TailLog([]byte(text), cv.MaxFailureLogBytes, nil)
}

// readLogTail reads a step's log, keeping only the last 2*MaxFailureLogBytes.
// cut says that the log was longer than that.
func (s *Service) readLogTail(ctx context.Context, build core.BuildID, step int) (buf []byte, cut bool) {
	keep := 2 * cv.MaxFailureLogBytes
	var after int64
	for i := 0; i < logReadLimit; i++ {
		chunk, next, err := s.d.Logs.StepLog(ctx, build, step, after)
		if err != nil {
			break
		}
		buf = append(buf, chunk...)
		if len(buf) > keep {
			buf = append([]byte(nil), buf[len(buf)-keep:]...)
			cut = true
		}
		if len(chunk) == 0 || next <= after {
			break
		}
		after = next
	}
	return buf, cut
}

// failureParts returns the name of the failed step (fallback when the build
// is not readable) and, for a non-zero exit, the redacted tail of its log.
func (s *Service) failureParts(ctx context.Context, failed cv.Failed, fallback string) (step, tail string) {
	step = fallback
	if st, gerr := s.d.Builds.Get(ctx, failed.Build); gerr == nil {
		if fin, isFin := st.(corebuilds.Finished); isFin && failed.Step >= 0 && failed.Step < len(fin.Spec.Steps) {
			step = fin.Spec.Steps[failed.Step].Name
		}
	}
	if _, isExit := failed.Reason.(cv.ExitCode); isExit && s.d.Logs != nil {
		raw, cut := s.readLogTail(ctx, failed.Build, failed.Step)
		tail = s.redactedTail(raw, cut)
	}
	return step, tail
}

// FailureDetail gives the name of the failed step and the redacted tail of
// its log, for display (VF-02). Both are empty for a record that has not
// failed; the step is empty when the build can no longer be read.
func (s *Service) FailureDetail(ctx context.Context, rec Record) (step, tail string) {
	failed, ok := rec.State.(cv.Failed)
	if !ok {
		return "", ""
	}
	return s.failureParts(ctx, failed, "")
}
