package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Stage audit and effect records of the Phase 6 executors (invariant 8). The
// lifecycle audit entries (create, confirm, execute, fail) come from Step; the
// entries here record what an executor did between "confirmed" and "executed":
// that it is about to write, and how the write ended. The "started" entry is
// committed BEFORE the external write, so a crash leaves a durable trace.

// stageAction is the audit action of every executor stage.
const stageAction = "proposal.effect"

// Stages written to the audit log. Plain words, never a path, text or token.
const (
	stageIgnoreStarted      = "ignore_started"
	stageIgnoreAppended     = "ignore_appended"
	stageIgnoreCreated      = "ignore_created"
	stageIgnoreAlreadyThere = "ignore_already_present"
	stageIgnoreRefused      = "ignore_refused"
	stageIgnoreUnsure       = "ignore_outcome_unknown"
	stageCommentStarted     = "comment_started"
	stageCommentPosted      = "comment_posted"
	stageCommentRejected    = "comment_rejected"
	stageCommentUnknown     = "comment_outcome_unknown"
	stageCommentAbsent      = "comment_not_found"
	stageCommentRecovered   = "comment_found_after_loss"
	stagePullStarted        = "pull_started"
	stagePullDone           = "pull_done"
	stagePullRefused        = "pull_refused"
	stagePullUnsure         = "pull_outcome_unknown"
)

// stageEntry is the audit entry of one executor stage in tx.
func (s *Service) stageEntry(ctx context.Context, tx *db.Tx, p coreprop.Proposal, attempt int, stage string, now time.Time) error {
	detail, err := json.Marshal(map[string]any{
		"kind": p.Action.Kind(), "task": p.Task, "repo": p.Repo, "attempt": attempt, "stage": stage,
	})
	if err != nil {
		return fmt.Errorf("audit detail: %w", err)
	}
	e, err := audit.Prepare(core.AuditEntry{
		Actor: "system", Action: stageAction, Subject: "proposal:" + string(p.ID), Detail: detail,
	}, s.cfg.Redactor, core.AuditID(s.cfg.NewID("aud")), now)
	if err != nil {
		return fmt.Errorf("prepare audit entry: %w", err)
	}
	return tx.AppendAudit(ctx, e)
}

// auditStage commits one stage entry on its own.
func (s *Service) auditStage(ctx context.Context, p coreprop.Proposal, attempt int, stage string) error {
	now := s.now()
	return s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
		return s.stageEntry(ctx, tx, p, attempt, stage, now)
	})
}

// auditStageBestEffort writes a stage entry that follows an effect that already
// happened: a failure to audit it is logged, and must not turn a done effect
// into a failure.
func (s *Service) auditStageBestEffort(ctx context.Context, p coreprop.Proposal, attempt int, stage string) {
	// The effect has happened; record it even when the executor's context ended.
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	if err := s.auditStage(ctx, p, attempt, stage); err != nil {
		s.cfg.Logger.Error("could not audit a proposal stage", "proposal", p.ID, "stage", stage, "error", s.redact(err.Error()))
	}
}

// errEffectExists: the effect of this attempt was already started (a second run
// of the same attempt); nothing may be sent again.
var errEffectExists = errors.New("the effect of this attempt was already started")

// beginEffect records the intent of a comment effect and its audit entry in one
// transaction, BEFORE anything is sent. A second intent for the same attempt is
// errEffectExists.
func (s *Service) beginEffect(ctx context.Context, p coreprop.Proposal, attempt int, marker, stage string) error {
	now := s.now()
	err := s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.InsertEffect(ctx, db.EffectRecord{
			Proposal: p.ID, Attempt: attempt, State: db.EffectIntent, Marker: marker, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return s.stageEntry(ctx, tx, p, attempt, stage, now)
	})
	if errors.Is(err, db.ErrConflict) {
		return errEffectExists
	}
	return err
}

// settleEffect moves an effect to a new state, with its audit entry, in one
// transaction. It uses a context that outlives cancellation: the effect has
// already happened (or was attempted) and its outcome must be recorded.
func (s *Service) settleEffect(ctx context.Context, p coreprop.Proposal, e db.EffectRecord, state db.EffectState, receiptKind string, receipt json.RawMessage, remoteID, stage string) error {
	ctx = context.WithoutCancel(ctx)
	now := s.now()
	cur, err := s.cfg.Store.GetEffect(ctx, e.ID)
	if err != nil {
		return fmt.Errorf("read effect %s: %w", e.ID, err)
	}
	return s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
		if _, err := tx.SettleEffect(ctx, cur.ID, state, receiptKind, receipt, remoteID, cur.Version, now); err != nil {
			return err
		}
		return s.stageEntry(ctx, tx, p, e.Attempt, stage, now)
	})
}

// userMessager is implemented by errors that carry text written for the user.
type userMessager interface{ UserMessage() string }

// userText returns the plain text of an error that has some, or fallback.
func userText(err error, fallback string) string {
	var um userMessager
	if errors.As(err, &um) {
		if m := um.UserMessage(); m != "" {
			return m
		}
	}
	return fallback
}

func isContextEnd(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// portMissing says in plain words why a Phase 6 action cannot be created: the
// capability that would run it is not configured. No proposal is created that
// cannot run (no false success).
func (s *Service) portMissing(a coreprop.Action) string {
	switch a.(type) {
	case coreprop.IgnoreEntries:
		if s.cfg.Ignore == nil {
			return "Editing .gitignore is not set up in this build of Yggdrasil yet."
		}
	case coreprop.ReviewComment:
		if s.cfg.Comments == nil {
			return "Posting review comments is not set up in this build of Yggdrasil yet."
		}
	case coreprop.PullFF:
		if s.cfg.Pull == nil {
			return "Pulling from the workspace is not set up in this build of Yggdrasil yet."
		}
	}
	return ""
}
