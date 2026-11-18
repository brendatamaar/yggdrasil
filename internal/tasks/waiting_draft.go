package tasks

import (
	"context"
	"errors"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// WaitingDraft is a diagnosis brief saved as a draft for a repo whose folder
// cannot be read (AC-01, E6). The brief is bounded and redacted by the caller
// and redacted again here.
type WaitingDraft struct {
	Project core.ProjectID
	Repo    core.RepoID
	Title   string
	Brief   string
	Agent   string
	Model   string
}

// SaveWaitingDraft creates ONE ordinary draft task in Ask mode for a tracked
// READY repo, with no turn, process, worktree or agent call, idempotent per
// request ID and payload digest. State, request link and audit entry are one
// transaction. Starting it later is the ordinary StartDraft/Message path, so
// admission, the trust scan and the Ask integrity check run then, never now.
func (s *Service) SaveWaitingDraft(ctx context.Context, id core.RequestID, d WaitingDraft) (core.TaskID, error) {
	if s.isClosed() {
		return "", &RejectedError{Reason: "shutting_down", Message: "Yggdrasil is shutting down."}
	}
	raw := coretasks.WaitingDraftSpec{
		RequestID: id, Project: d.Project, Repo: d.Repo, Title: d.Title, Brief: d.Brief, Agent: d.Agent, Model: d.Model}
	spec, err := coretasks.ParseWaitingDraftSpec(raw)
	if err != nil {
		return "", err
	}
	// An anchor is identified by its repo alone: its fixed payload is the same
	// whatever the project says, so only the repo and kind are digested.
	digestSpec := spec
	if spec.Agent == coretasks.AnchorAgent {
		digestSpec.Project = ""
	}
	digest, err := payloadDigest("waiting_draft", digestSpec)
	if err != nil {
		return "", err
	}
	// Redact after digesting so a changed immutable input still refuses.
	spec.Title = s.c.Redactor.Redact(spec.Title)
	spec.Brief = s.c.Redactor.Redact(spec.Brief)
	if spec, err = coretasks.ParseWaitingDraftSpec(spec); err != nil {
		return "", err
	}
	taskID := core.TaskID(s.c.NewID("tsk"))
	now := s.c.Now().UTC()
	for attempt := 0; ; attempt++ {
		got, err := s.saveWaitingOnce(ctx, spec, digest, taskID, now)
		if errors.Is(err, db.ErrConflict) && attempt == 0 {
			continue
		}
		return got, err
	}
}

// saveWaitingOnce reads the facts inside the transaction, asks the core and
// interprets the outcome. It makes no decision of its own.
func (s *Service) saveWaitingOnce(ctx context.Context, spec coretasks.WaitingDraftSpec, digest string, taskID core.TaskID, now time.Time) (core.TaskID, error) {
	var saved core.TaskID
	err := s.c.Store.Commit(ctx, func(tx *db.Tx) error {
		facts, err := s.draftFacts(ctx, tx, spec.Project, spec.Repo)
		if err != nil {
			return err
		}
		facts.TaskID, facts.Now, facts.Digest = taskID, now, digest
		facts.Replay = coretasks.NoReplay{}
		prior, priorDigest, err := tx.GetTaskSubmission(ctx, spec.RequestID)
		switch {
		case err == nil:
			facts.Replay = coretasks.Replayed{Task: prior, Digest: priorDigest}
		case !errors.Is(err, db.ErrNotFound):
			return err
		}
		switch out := coretasks.PlanWaitingDraft(spec, facts).(type) {
		case coretasks.DraftReplayed:
			saved = out.Task
			return nil
		case coretasks.DraftCreate:
			task, err := tx.CreateTask(ctx, out.Task)
			if err != nil {
				return err
			}
			saved = task.ID
		case coretasks.DraftRefuse:
			if out.Reason == coretasks.DraftPayloadChanged {
				return db.ErrDigestMismatch
			}
			return &RejectedError{Reason: string(out.Reason), Message: out.Reason.WaitingMessage()}
		case coretasks.DraftExists:
			return errors.New("waiting draft planned an issue duplicate")
		}
		if err := tx.LinkTaskSubmission(ctx, spec.RequestID, digest, saved); err != nil {
			return err
		}
		return s.appendWaitingDraftAudit(ctx, tx, saved, spec.Repo)
	})
	if err != nil {
		return "", err
	}
	return saved, nil
}
