package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
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
	trusted, err := s.d.Store.RepoTrusted(ctx, rec.Repo)
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
				Facts: facts, Plan: plan, BootBuild: core.BuildID(s.d.IDs("bld")), VerifyBuild: core.BuildID(s.d.IDs("bld")), Now: s.now(),
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
	report := cv.FailureReport{
		Repo: rec.Repo, Stage: failed.Stage, Step: fmt.Sprintf("step %d", failed.Step+1), Reason: failed.Reason,
	}
	if st, gerr := s.d.Builds.Get(ctx, failed.Build); gerr == nil {
		if fin, isFin := st.(corebuilds.Finished); isFin && failed.Step >= 0 && failed.Step < len(fin.Spec.Steps) {
			report.Step = fin.Spec.Steps[failed.Step].Name
		}
	}
	if _, isExit := failed.Reason.(cv.ExitCode); isExit && s.d.Logs != nil {
		report.LogTail = TailLog(s.readLogTail(ctx, failed.Build, failed.Step), cv.MaxFailureLogBytes, s.d.Redactor.Redact)
	}
	text, err := cv.ComposeFailure(report)
	if err != nil {
		return apperr.New(apperr.Invalid, "Yggdrasil couldn't write the message for the agent.", err)
	}
	if err := s.d.Messenger.SendMessage(ctx, rec.Task, text); err != nil {
		return err
	}
	// The new turn is audited by tasks; this entry ties it to the verification.
	return s.auditSent(ctx, rec, failed)
}

func (s *Service) auditSent(ctx context.Context, rec Record, failed cv.Failed) error {
	now := s.now()
	detail, err := json.Marshal(map[string]string{"task": string(rec.Task), "repo": string(rec.Repo), "turn": string(failed.Turn), "outcome": string(failed.Stage)})
	if err != nil {
		return fmt.Errorf("audit detail: %w", err)
	}
	e := core.AuditEntry{Actor: userActor, Action: "verify.send_failure", Subject: "verification:" + string(rec.ID), Detail: detail}
	err = s.d.Store.Commit(ctx, func(tx Tx) error {
		pe, perr := audit.Prepare(e, s.d.Redactor, core.AuditID(s.d.IDs("aud")), now)
		if perr != nil {
			return perr
		}
		return tx.AppendAudit(ctx, pe)
	})
	if err != nil {
		// The message is already sent; do not report the send as failed.
		s.d.Log.Error("could not audit a sent failure", "verification", rec.ID, "err", err)
	}
	return nil
}

// readLogTail reads a step's log, keeping only the last bytes.
func (s *Service) readLogTail(ctx context.Context, build core.BuildID, step int) []byte {
	keep := 2 * cv.MaxFailureLogBytes
	var buf []byte
	var after int64
	for i := 0; i < logReadLimit; i++ {
		chunk, next, err := s.d.Logs.StepLog(ctx, build, step, after)
		if err != nil {
			break
		}
		buf = append(buf, chunk...)
		if len(buf) > keep {
			buf = append([]byte(nil), buf[len(buf)-keep:]...)
		}
		if len(chunk) == 0 || next <= after {
			break
		}
		after = next
	}
	return buf
}
