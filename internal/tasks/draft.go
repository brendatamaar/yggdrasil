package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

var _ DraftService = (*Service)(nil)
var _ RequestSubmitter = (*Service)(nil)

type submissionLink struct {
	ID     core.RequestID
	Digest string
}

// payloadDigest separates request kinds so a key cannot be reused across ports.
func payloadDigest(kind string, value any) (string, error) {
	raw, err := json.Marshal(struct {
		Kind    string
		Payload any
	}{kind, value})
	if err != nil {
		return "", fmt.Errorf("encode task submission: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// LookupRequest reconciles a committed submission without admitting any work.
func (s *Service) LookupRequest(ctx context.Context, id core.RequestID) (core.TaskID, error) {
	task, _, err := s.c.Store.GetTaskSubmission(ctx, id)
	return task, err
}

// SubmitWithRequest admits a new task once, with its immutable payload and link
// stored atomically with the first turn and audit. Existing tasks use Message.
func (s *Service) SubmitWithRequest(ctx context.Context, id core.RequestID, req MessageRequest) (MessageResult, error) {
	if id.Validate() != nil || len(id) > 100 || req.TaskID != "" {
		return MessageResult{}, &coretasks.UserError{Message: "Choose a valid request ID for a new task."}
	}
	digest, err := payloadDigest("message", req)
	if err != nil {
		return MessageResult{}, err
	}
	link := submissionLink{ID: id, Digest: digest}
	result, err := s.submissionResult(ctx, link)
	if !errors.Is(err, db.ErrNotFound) {
		return result, err
	}
	return s.message(ctx, req, &link, false)
}

func (s *Service) submissionResult(ctx context.Context, link submissionLink) (MessageResult, error) {
	id, digest, err := s.c.Store.GetTaskSubmission(ctx, link.ID)
	if err != nil {
		return MessageResult{}, err
	}
	if digest != link.Digest {
		return MessageResult{}, db.ErrDigestMismatch
	}
	task, err := s.c.Store.GetTask(ctx, id)
	if err != nil {
		return MessageResult{}, err
	}
	turns, err := s.c.Store.ListTaskTurns(ctx, id)
	if err != nil {
		return MessageResult{}, err
	}
	if len(turns) == 0 {
		return MessageResult{}, errors.New("committed message submission has no first turn")
	}
	result := MessageResult{Task: task, Turn: turns[0].ID}
	switch turns[0].Mode {
	case core.ModeCurrentBranch:
		result.Warning = "edits_main_checkout"
	case core.ModeProject:
		result.Warning = "edits_all_repos"
	}
	return result, nil
}

// ImportIssueDraft persists only task data, provenance, request link and audit.
// The unique project/issue key arbitrates imports racing with different IDs.
func (s *Service) ImportIssueDraft(ctx context.Context, spec coretasks.DraftSpec) (DraftResult, error) {
	if s.isClosed() {
		return DraftResult{}, &RejectedError{Reason: "shutting_down", Message: "Yggdrasil is shutting down."}
	}
	spec, err := coretasks.ParseDraftSpec(spec)
	if err != nil {
		return DraftResult{}, err
	}
	digest, err := payloadDigest("issue", spec)
	if err != nil {
		return DraftResult{}, err
	}
	// Redact after digesting so changed immutable input still refuses.
	spec.Title = s.c.Redactor.Redact(spec.Title)
	spec.Brief = s.c.Redactor.Redact(spec.Brief)
	spec, err = coretasks.ParseDraftSpec(spec)
	if err != nil {
		return DraftResult{}, err
	}
	for try := 0; try < 3; try++ {
		old, lookupErr := s.c.Store.GetDraftByDedupKey(ctx, spec.Project, spec.Issue.DedupKey())
		if lookupErr != nil && !errors.Is(lookupErr, db.ErrNotFound) {
			return DraftResult{}, lookupErr
		}
		facts := coretasks.DraftFacts{RepoTracked: true, RepoReady: true, TaskID: core.TaskID(s.c.NewID("tsk")), Now: s.c.Now().UTC()}
		if lookupErr == nil {
			facts.Existing = old.ID
		}
		var result DraftResult
		err = s.c.Store.Commit(ctx, func(tx *db.Tx) error {
			if _, err := tx.GetTrackedReadyRepo(ctx, spec.Project, spec.Repo); err != nil {
				return err
			}
			_, priorDigest, err := tx.GetTaskSubmission(ctx, spec.RequestID)
			if err == nil {
				if priorDigest != digest {
					return db.ErrDigestMismatch
				}
				return db.ErrConflict // rollback, then reload the original outside Commit
			}
			if !errors.Is(err, db.ErrNotFound) {
				return err
			}
			switch out := coretasks.PlanDraft(spec, facts).(type) {
			case coretasks.DraftCreate:
				task, err := tx.CreateTask(ctx, out.Task)
				if err != nil {
					return err
				}
				result = DraftResult{Task: task, Created: true}
			case coretasks.DraftExists:
				result = DraftResult{Task: old}
			case coretasks.DraftRefuse:
				return &RejectedError{Reason: string(out.Reason), Message: "Track a ready repo before importing an issue."}
			}
			if err := tx.LinkTaskSubmission(ctx, spec.RequestID, digest, result.Task.ID); err != nil {
				return err
			}
			return s.appendImportAudit(ctx, tx, result.Task.ID, result.Created)
		})
		if !errors.Is(err, db.ErrConflict) {
			if err != nil {
				return DraftResult{}, err
			}
			return result, nil
		}
		id, prior, lookupErr := s.c.Store.GetTaskSubmission(ctx, spec.RequestID)
		if lookupErr == nil {
			if prior != digest {
				return DraftResult{}, db.ErrDigestMismatch
			}
			task, err := s.c.Store.GetTask(ctx, id)
			return DraftResult{Task: task}, err
		}
		if !errors.Is(lookupErr, db.ErrNotFound) {
			return DraftResult{}, lookupErr
		}
	}
	return DraftResult{}, fmt.Errorf("import issue: %w", db.ErrConflict)
}

// StartDraft submits the stored brief through ordinary admission. It cannot
// start setup drafts or silently change the stored agent/model identity.
func (s *Service) StartDraft(ctx context.Context, start coretasks.DraftStart, agent string) (MessageResult, error) {
	if start.Task.Validate() != nil {
		return MessageResult{}, &coretasks.UserError{Message: "Choose a valid draft task."}
	}
	task, err := s.c.Store.GetTask(ctx, start.Task)
	if err != nil {
		return MessageResult{}, err
	}
	if task.Source != nil && task.Source.Kind == core.SourceRepoSetup {
		return MessageResult{}, &coretasks.UserError{Message: "Repo setup drafts cannot start an agent."}
	}
	if (agent != "" && agent != task.Agent) || (start.Model != "" && start.Model != task.Model) {
		return MessageResult{}, &coretasks.UserError{Message: "Start this draft with its stored agent and model."}
	}
	return s.message(ctx, MessageRequest{TaskID: task.ID, Prompt: task.Prompt, Mode: string(start.Mode), OptIn: start.OptIn}, nil, true)
}
