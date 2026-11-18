package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	if id.Validate() != nil || len(id) > 100 || req.TaskID != "" || coretasks.HasReservedPrefix(id) {
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
	taskID := core.TaskID(s.c.NewID("tsk"))
	now := s.c.Now().UTC()
	// A racing writer can still turn a write into a conflict; the second pass
	// reads the committed row as a fact and lets the core decide again.
	for attempt := 0; ; attempt++ {
		result, err := s.importOnce(ctx, spec, digest, taskID, now)
		if errors.Is(err, db.ErrConflict) && attempt == 0 {
			continue
		}
		return result, err
	}
}

// importOnce reads the facts of one import inside a transaction, asks the core
// what to do and interprets the outcome. It makes no decision of its own.
func (s *Service) importOnce(ctx context.Context, spec coretasks.DraftSpec, digest string, taskID core.TaskID, now time.Time) (DraftResult, error) {
	var result DraftResult
	err := s.c.Store.Commit(ctx, func(tx *db.Tx) error {
		// Everything the decision depends on is read inside this transaction.
		// The store has one write connection, so nothing can commit between
		// these reads and the writes below. The reads go through the read
		// pool, which sees only committed rows, and no write has happened yet.
		facts, err := s.draftFacts(ctx, tx, spec.Project, spec.Repo)
		if err != nil {
			return err
		}
		facts.TaskID, facts.Now, facts.Digest = taskID, now, digest
		facts.Replay = coretasks.NoReplay{}
		replayTask, priorDigest, err := tx.GetTaskSubmission(ctx, spec.RequestID)
		switch {
		case err == nil:
			facts.Replay = coretasks.Replayed{Task: replayTask, Digest: priorDigest}
		case !errors.Is(err, db.ErrNotFound):
			return err
		}
		// The issue's existing draft is read only for a first request in a
		// repo of the project: any other outcome ignores it, so the read
		// would be wasted IO (and the core refuses out-of-scope facts anyway).
		var old core.Task
		if _, replayed := facts.Replay.(coretasks.Replayed); !replayed && facts.RepoTracked && facts.RepoReady {
			old, err = s.c.Store.GetDraftByDedupKey(ctx, spec.Project, spec.Issue.DedupKey())
			switch {
			case err == nil:
				facts.Existing = old.ID
			case !errors.Is(err, db.ErrNotFound):
				return err
			}
		}
		switch out := coretasks.PlanDraft(spec, facts).(type) {
		case coretasks.DraftReplayed:
			// Nothing to write: return the draft the request produced.
			task, err := s.c.Store.GetTask(ctx, out.Task)
			if err != nil {
				return err
			}
			result = DraftResult{Task: task}
			return nil
		case coretasks.DraftCreate:
			task, err := tx.CreateTask(ctx, out.Task)
			if err != nil {
				return err
			}
			result = DraftResult{Task: task, Created: true}
		case coretasks.DraftExists:
			result = DraftResult{Task: old}
		case coretasks.DraftRefuse:
			if out.Reason == coretasks.DraftPayloadChanged {
				return db.ErrDigestMismatch
			}
			return &RejectedError{Reason: string(out.Reason), Message: out.Reason.Message()}
		}
		if err := tx.LinkTaskSubmission(ctx, spec.RequestID, digest, result.Task.ID); err != nil {
			return err
		}
		return s.appendImportAudit(ctx, tx, result.Task.ID, result.Created)
	})
	if err != nil {
		return DraftResult{}, err
	}
	return result, nil
}

// draftFacts reads the repo scope of an import. Only not-found maps to a
// fact: an unknown, untracked or foreign-project repo is "not tracked" (the
// same answer for all three, so nothing outside the project is revealed), and
// a reserved repo of this project is "tracked, not ready". Any other error
// stays an error.
func (s *Service) draftFacts(ctx context.Context, tx *db.Tx, project core.ProjectID, repo core.RepoID) (coretasks.DraftFacts, error) {
	_, err := tx.GetTrackedReadyRepo(ctx, project, repo)
	if err == nil {
		return coretasks.DraftFacts{RepoTracked: true, RepoReady: true}, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return coretasks.DraftFacts{}, fmt.Errorf("read repo for issue import: %w", err)
	}
	reserved, err := s.c.Store.GetReservedRepo(ctx, repo)
	switch {
	case err == nil && reserved.ProjectID == project:
		return coretasks.DraftFacts{RepoTracked: true}, nil
	case err == nil || errors.Is(err, db.ErrNotFound):
		return coretasks.DraftFacts{}, nil
	}
	return coretasks.DraftFacts{}, fmt.Errorf("read repo for issue import: %w", err)
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
	if refusal := coretasks.StartRefusal(task.Source); refusal != nil {
		return MessageResult{}, refusal
	}
	if (agent != "" && agent != task.Agent) || (start.Model != "" && start.Model != task.Model) {
		return MessageResult{}, &coretasks.UserError{Message: "Start this draft with its stored agent and model."}
	}
	return s.message(ctx, MessageRequest{TaskID: task.ID, Prompt: task.Prompt, Mode: string(start.Mode), OptIn: start.OptIn}, nil, true)
}
