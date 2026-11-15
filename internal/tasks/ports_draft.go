package tasks

// Phase 6 declarations of the tasks package (docs/design/phase-6-actions.md and
// phase-6-newrepos.md): draft creation from an issue, the explicit start of a
// draft, a RequestID-aware submission, and the repo-setup draft. Implemented by
// *Service using the ordinary admission path. Existing
// MessageRequest and Message are unchanged.

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// DraftResult is what an import produced.
type DraftResult struct {
	Task core.Task
	// Created is false when the issue was already imported and Task is the
	// first draft.
	Created bool
}

// DraftService is implemented by *Service. ImportIssueDraft creates one
// ordinary task in the draft status with provenance and a dedup key, linked to
// a tracked READY repo, in one Store.Commit with its audit entry; it creates
// no turn, process or worktree and calls no agent. StartDraft is the explicit
// submission: it goes through guard.Admit, the trust scan, the shared main-
// checkout lock and the queue exactly like Message, and refuses a repo-setup
// draft (core.SourceRepoSetup) with a *coretasks.UserError.
type DraftService interface {
	ImportIssueDraft(ctx context.Context, spec coretasks.DraftSpec) (DraftResult, error)
	StartDraft(ctx context.Context, start coretasks.DraftStart, agent string) (MessageResult, error)
}

// RequestSubmitter is the RequestID-aware submission the actions package uses
// for diagnose, fix, review and polish tasks. The request ID is stored with the
// created task in the same Store.Commit, so a repeated ID returns the first
// result and a crash after admission is reconciled by looking the ID up, never
// by submitting again. LookupRequest is the read-only reconciliation: it
// returns the task linked to id, or db.ErrNotFound.
type RequestSubmitter interface {
	SubmitWithRequest(ctx context.Context, id core.RequestID, req MessageRequest) (MessageResult, error)
	LookupRequest(ctx context.Context, id core.RequestID) (core.TaskID, error)
}
