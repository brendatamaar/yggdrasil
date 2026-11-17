package newrepos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// extra is what the shell knows next to an input: who acted, and the proposals
// the effect records belong to.
type extra struct {
	actor string
	// proposal is the initialization proposal being attached (ProposalCreated).
	proposal core.ProposalID
	// publishProposal is the publish proposal the publish effects belong to.
	publishProposal core.ProposalID
}

// applied is what one load -> Step -> commit produced.
type applied struct {
	before  corenewrepos.Operation
	after   corenewrepos.Operation
	changed bool
	// effects are the commands the caller runs after the commit.
	effects []corenewrepos.Command
	explain []corenewrepos.StopReason
	cancel  bool
}

func hostInstance(spec corenewrepos.PublishSpec) corehosts.InstanceID {
	return corehosts.InstanceID(spec.HostInstance)
}

// apply is the one transition path: load the stored operation, call the core
// Step, and commit the new state, the intents, the receipts and the audit
// entries in ONE Store.Commit. Only after the commit does the caller run the
// returned effect commands, so an intent is always durable before its effect.
func (s *Service) apply(ctx context.Context, id core.OperationID, mk func(db.ReservationRecord) (corenewrepos.Input, extra)) (applied, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	rec, err := s.d.Store.GetReservation(ctx, id)
	if err != nil {
		return applied{}, fmt.Errorf("load new repo %s: %w", id, err)
	}
	in, ex := mk(rec)
	next, cmds := corenewrepos.Step(rec.Operation, in)
	res := applied{before: rec.Operation, after: next}
	records := false
	for _, c := range cmds {
		switch c := c.(type) {
		case corenewrepos.PersistIntent, corenewrepos.PersistReceipt, corenewrepos.PersistFailure,
			corenewrepos.Audit, corenewrepos.PersistReservationAndSetupDraft:
			records = true
		case corenewrepos.RunInitStage, corenewrepos.RunPublishStage, corenewrepos.ReconcileStage:
			res.effects = append(res.effects, c)
		case corenewrepos.CancelWorker:
			res.cancel = true
		case corenewrepos.Explain:
			res.explain = append(res.explain, c.Reason)
		case corenewrepos.PublishPreview:
		}
	}
	if !records {
		return res, nil
	}
	effects, err := s.d.Store.ListEffectsByOperation(ctx, id)
	if err != nil {
		return applied{}, fmt.Errorf("load effects of %s: %w", id, err)
	}
	now := s.d.Clock()
	err = s.d.Store.Commit(ctx, func(tx *db.Tx) error {
		if err := s.recordEffects(ctx, tx, rec, next, cmds, effects, ex, now); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, ex.actor, cmds, now); err != nil {
			return err
		}
		if _, ready := next.State.(corenewrepos.Ready); ready {
			if _, was := rec.Operation.State.(corenewrepos.Ready); !was {
				// FinalizeTracking: the reserved repo becomes an ordinary
				// ready repo in the same commit as the Ready state.
				if err := tx.MarkRepoReady(ctx, rec.Operation.Repo, corenewrepos.DefaultBranch); err != nil {
					return fmt.Errorf("finalize tracking: %w", err)
				}
			}
		}
		proposal := rec.Proposal
		if ex.proposal != "" {
			proposal = ex.proposal
		}
		if _, err := tx.SaveReservation(ctx, id, next.State, proposal, rec.Operation.Version, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return applied{}, fmt.Errorf("save new repo %s: %w", id, err)
	}
	res.changed = true
	s.announce(id, next.State)
	return res, nil
}

// audit writes every Audit command as an audit entry in tx (invariant 8). The
// detail is two codes; no error text, path or URL goes in.
func (s *Service) audit(ctx context.Context, tx *db.Tx, actor string, cmds []corenewrepos.Command, now time.Time) error {
	if actor == "" {
		actor = "system"
	}
	for _, c := range cmds {
		a, ok := c.(corenewrepos.Audit)
		if !ok {
			continue
		}
		detail, err := json.Marshal(map[string]string{"stage": a.Stage, "outcome": a.Outcome})
		if err != nil {
			return err
		}
		e, err := audit.Prepare(core.AuditEntry{
			Actor: actor, Action: a.Action, Subject: "newrepo:" + string(a.Operation), Detail: detail,
		}, redactFunc(s.d.Redact), core.AuditID(s.d.IDs("aud")), now)
		if err != nil {
			return fmt.Errorf("prepare audit entry: %w", err)
		}
		if err := tx.AppendAudit(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

func findEffect(effects []db.EffectRecord, stage string, attempt int) (db.EffectRecord, bool) {
	for _, e := range effects {
		if e.Stage == stage && e.Attempt == attempt {
			return e, true
		}
	}
	return db.EffectRecord{}, false
}

// recordEffects writes, in tx, the effect records the commands call for: the
// intent of every stage about to run (before it runs), the settlement of the
// stage that just finished or became uncertain.
func (s *Service) recordEffects(ctx context.Context, tx *db.Tx, rec db.ReservationRecord, next corenewrepos.Operation,
	cmds []corenewrepos.Command, effects []db.EffectRecord, ex extra, now time.Time) error {
	id := rec.Operation.ID
	inserted := map[string]bool{}
	insert := func(stage corenewrepos.Stage, attempt int) error {
		key := fmt.Sprintf("%s#%d", stage.Name(), attempt)
		if inserted[key] {
			return nil
		}
		inserted[key] = true
		proposal, marker := rec.Proposal, ""
		if _, isPublish := stage.(corenewrepos.PublishStage); isPublish {
			proposal = s.publishProposal(id, effects, ex)
			if p, ok := next.State.(corenewrepos.Publishing); ok && stage == corenewrepos.StageCreateRemote {
				marker = p.Spec.Marker
			}
		}
		if err := proposal.Validate(); err != nil {
			return fmt.Errorf("no proposal owns stage %s: %w", stage.Name(), err)
		}
		return tx.InsertEffect(ctx, db.EffectRecord{Proposal: proposal, Operation: id, Stage: stage.Name(), Attempt: attempt,
			State: db.EffectIntent, Marker: marker, CreatedAt: now})
	}
	settle := func(stage corenewrepos.Stage, attempt int, state db.EffectState, kind string, receipt json.RawMessage, remoteID string) error {
		e, ok := findEffect(effects, stage.Name(), attempt)
		if !ok {
			return nil // nothing was recorded for it (it never ran); nothing to settle
		}
		if e.State == db.EffectSucceeded || e.State == db.EffectFailed || (state == db.EffectUnknown && e.State == db.EffectUnknown) {
			return nil
		}
		_, err := tx.SettleEffect(ctx, e.ID, state, kind, receipt, remoteID, e.Version, now)
		return err
	}
	for _, c := range cmds {
		switch c := c.(type) {
		case corenewrepos.PersistIntent:
			if err := insert(c.Stage, c.Attempt); err != nil {
				return err
			}
		case corenewrepos.PersistReceipt:
			kind, data, err := db.EncodeReceipt(c.Receipt)
			if err != nil {
				return err
			}
			remoteID := ""
			if r, ok := c.Receipt.(corenewrepos.RemoteReceipt); ok {
				remoteID = r.RemoteID
			}
			if err := settle(c.Stage, c.Attempt, db.EffectSucceeded, kind, json.RawMessage(data), remoteID); err != nil {
				return err
			}
		case corenewrepos.PersistFailure:
			if c.Stage == nil {
				continue
			}
			switch st := next.State.(type) {
			case corenewrepos.Uncertain:
				if err := settle(c.Stage, st.Attempt, db.EffectUnknown, "", nil, ""); err != nil {
					return err
				}
			case corenewrepos.Stopped:
				// A conflict leaves the effect's real outcome unknown; every other
				// stop means the stage did not take effect.
				if st.Attempt > 0 && st.Reason != corenewrepos.StopConflict {
					if err := settle(c.Stage, st.Attempt, db.EffectFailed, "", nil, ""); err != nil {
						return err
					}
				}
			case corenewrepos.Reserved, corenewrepos.AwaitingConfirm, corenewrepos.Initializing, corenewrepos.Ready,
				corenewrepos.Publishing, corenewrepos.Published:
			}
		case corenewrepos.RunInitStage, corenewrepos.RunPublishStage, corenewrepos.ReconcileStage,
			corenewrepos.PersistReservationAndSetupDraft, corenewrepos.PublishPreview, corenewrepos.Audit,
			corenewrepos.CancelWorker, corenewrepos.Explain:
		}
	}
	// A stage that runs without its own PersistIntent (the next stage after a
	// receipt) still gets its intent in this commit, before it runs.
	for _, c := range cmds {
		switch c := c.(type) {
		case corenewrepos.RunInitStage:
			if err := insert(c.Stage, c.Attempt); err != nil {
				return err
			}
		case corenewrepos.RunPublishStage:
			if err := insert(c.Stage, c.Attempt); err != nil {
				return err
			}
		case corenewrepos.ReconcileStage, corenewrepos.PersistReservationAndSetupDraft, corenewrepos.PublishPreview,
			corenewrepos.Audit, corenewrepos.CancelWorker, corenewrepos.Explain, corenewrepos.PersistIntent,
			corenewrepos.PersistReceipt, corenewrepos.PersistFailure:
		}
	}
	return nil
}

// publishProposal is the proposal the publish effects of an operation belong
// to: the one being confirmed now, the one remembered from the preview, or the
// one an earlier publish effect already recorded.
func (s *Service) publishProposal(id core.OperationID, effects []db.EffectRecord, ex extra) core.ProposalID {
	if ex.publishProposal != "" {
		return ex.publishProposal
	}
	s.mu.Lock()
	p, ok := s.pending[id]
	s.mu.Unlock()
	if ok && p.proposal != "" {
		return p.proposal
	}
	for i := len(effects) - 1; i >= 0; i-- {
		if _, err := corenewrepos.ParseStage(effects[i].Stage); err == nil {
			if _, isPub := mustStage(effects[i].Stage).(corenewrepos.PublishStage); isPub {
				return effects[i].Proposal
			}
		}
	}
	return ""
}

func mustStage(name string) corenewrepos.Stage {
	st, err := corenewrepos.ParseStage(name)
	if err != nil {
		return nil
	}
	return st
}

// ---- hub ----

// announce publishes the new state through the hub after the commit, with the
// operation ID and the state code only.
func (s *Service) announce(op core.OperationID, st corenewrepos.State) {
	if s.d.Hub == nil {
		return
	}
	code, err := stateCode(st)
	if err != nil {
		return
	}
	s.d.Hub.PublishNewRepo(api.NewRepoChangedMessage{OperationID: op, State: code})
}

// stateCode maps a core state to the API state.
func stateCode(st corenewrepos.State) (api.NewRepoState, error) {
	switch st.(type) {
	case corenewrepos.Reserved:
		return api.NewRepoReserved, nil
	case corenewrepos.AwaitingConfirm:
		return api.NewRepoAwaitingConfirm, nil
	case corenewrepos.Initializing:
		return api.NewRepoInitializing, nil
	case corenewrepos.Ready:
		return api.NewRepoReady, nil
	case corenewrepos.Publishing:
		return api.NewRepoPublishing, nil
	case corenewrepos.Published:
		return api.NewRepoPublished, nil
	case corenewrepos.Uncertain:
		return api.NewRepoUncertain, nil
	case corenewrepos.Stopped:
		return api.NewRepoStopped, nil
	}
	return "", errors.New("unknown new repo state")
}

// redactFunc adapts the Deps redaction function to safety.Redactor.
type redactFunc func(string) string

func (f redactFunc) Redact(s string) string { return f(s) }
