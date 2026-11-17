package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// run is one pass of the dispatcher over one request: it holds the core state,
// feeds the core one input at a time and interprets the commands it returns.
// A run is used by one goroutine at a time.
type run struct {
	s *Service
	// sub is the core state, rec the stored row (nil until the intent is stored).
	sub coreactions.Submission
	rec *db.ActionRecord
	l   *loaded

	// preview skips every write: the run only loads and presents.
	preview bool
	// quiet skips the hub publish (a background retry).
	quiet bool

	expected    string
	expectedPin string

	// release frees the request lock; the effect goroutine takes it over.
	release func()
	handed  bool
	done    chan struct{}

	err    error
	detail string
	// dup: the row existed already; nothing is dispatched.
	dup bool
}

func terminal(st coreactions.State) bool {
	switch st.(type) {
	case coreactions.Completed, coreactions.Refused, coreactions.Cancelled:
		return true
	}
	return false
}

// feed is (state, commands) = Step(state, input), then the commands.
func (r *run) feed(ctx context.Context, in coreactions.Input) {
	if r.err != nil {
		return
	}
	next, cmds := coreactions.Step(r.sub, in)
	r.sub = next
	r.exec(ctx, cmds)
}

func (r *run) exec(ctx context.Context, cmds []coreactions.Command) {
	var audits []coreactions.Audit
	hasIntent := false
	for _, c := range cmds {
		switch c := c.(type) {
		case coreactions.Audit:
			audits = append(audits, c)
		case coreactions.PersistIntent:
			hasIntent = true
		}
	}
	if r.preview {
		audits = nil
	}
	// Audit entries go with the state change they describe, in one commit. A
	// list with no state change writes them on their own, in order.
	needSettle := !r.preview && r.rec != nil && r.rec.State == db.ActionSubmitting && terminal(r.sub.State)
	switch {
	case needSettle:
		if r.err = r.settle(ctx, audits); r.err != nil {
			return
		}
		audits = nil
	case len(audits) > 0 && !hasIntent:
		if r.err = r.commit(ctx, audits, nil); r.err != nil {
			return
		}
		audits = nil
	}
	for _, c := range cmds {
		switch c := c.(type) {
		case coreactions.LoadFactsAndContext:
			r.load(ctx, c)
		case coreactions.PresentPreview:
			// The preview is the Ready state itself; callers read it from there.
		case coreactions.Explain:
			if r.detail == "" {
				r.detail = message(c.Reason)
			}
		case coreactions.PersistIntent:
			if r.preview {
				continue
			}
			if r.err = r.persistIntent(ctx, c, audits); r.err != nil {
				return
			}
			audits = nil
		case coreactions.Dispatch:
			r.startDispatch(c)
			return
		case coreactions.ReconcileDispatch:
			r.reconcile(ctx, c)
		case coreactions.Publish:
			r.publish()
		case coreactions.PersistResult, coreactions.Audit, coreactions.CancelPreparation:
			// PersistResult is the settle above; audits are written with it.
		}
		if r.err != nil {
			return
		}
	}
}

func (r *run) load(ctx context.Context, c coreactions.LoadFactsAndContext) {
	l, why := r.s.prepare(ctx, c.Request)
	if why != "" {
		r.feed(ctx, coreactions.PrepareFailed{Attempt: c.Attempt, Reason: why})
		return
	}
	r.l = l
	r.feed(ctx, coreactions.Prepared{Attempt: c.Attempt, Facts: l.facts, Context: l.pctx})
}

func (r *run) publish() {
	if r.preview || r.quiet || r.rec == nil || r.s.d.Hub == nil {
		return
	}
	// After the commit; IDs and a state only, never a prompt or context.
	r.s.d.Hub.PublishAction(api.ActionChangedMessage{RequestID: r.sub.Request.RequestID, State: apiState(r.rec.State)})
}

func (r *run) auditEntry(a coreactions.Audit) (core.AuditEntry, error) {
	detail, err := json.Marshal(map[string]string{
		"request": string(a.Request), "kind": string(a.Kind), "outcome": a.Outcome, "repo": string(r.sub.Request.RepoID),
	})
	if err != nil {
		return core.AuditEntry{}, fmt.Errorf("audit detail: %w", err)
	}
	return audit.Prepare(core.AuditEntry{Actor: "user", Action: a.Action, Subject: "action:" + string(a.Request), Detail: detail},
		r.s.d.Redactor, core.AuditID(r.s.d.IDs("aud")), r.s.d.Clock())
}

func (r *run) prepareAudits(as []coreactions.Audit) ([]core.AuditEntry, error) {
	out := make([]core.AuditEntry, 0, len(as))
	for _, a := range as {
		e, err := r.auditEntry(a)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// commit writes audit entries (and an optional state change) in one
// transaction.
func (r *run) commit(ctx context.Context, as []coreactions.Audit, state func(*db.Tx) error) error {
	entries, err := r.prepareAudits(as)
	if err != nil {
		return err
	}
	if err := r.s.d.Requests.Commit(ctx, func(tx *db.Tx) error {
		if state != nil {
			if err := state(tx); err != nil {
				return err
			}
		}
		for _, e := range entries {
			if err := tx.AppendAudit(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("record action %s: %w", r.sub.Request.RequestID, err)
	}
	return nil
}

// persistIntent stores the request and its intent before any effect. If the
// row already exists (another process won the race) nothing is dispatched.
func (r *run) persistIntent(ctx context.Context, c coreactions.PersistIntent, as []coreactions.Audit) error {
	now := r.s.d.Clock()
	rec := db.ActionRecord{
		Request: c.Request, Digest: c.Digest, State: db.ActionSubmitting, Attempt: c.Attempt, Key: c.Key, CreatedAt: now.UTC(),
	}
	var stored db.ActionRecord
	created := false
	err := r.commit(ctx, as, func(tx *db.Tx) error {
		var err error
		stored, created, err = tx.BeginActionRequest(ctx, rec)
		if err == nil && !created {
			return errAlreadyStored
		}
		return err
	})
	if errors.Is(err, errAlreadyStored) {
		// The same request was stored first: answer with it, start nothing.
		r.rec = &stored
		r.dup = true
		return nil
	}
	if err != nil {
		if errors.Is(err, db.ErrDigestMismatch) {
			return db.ErrDigestMismatch
		}
		return err
	}
	rec.Version = 0
	r.rec = &rec
	return nil
}

var errAlreadyStored = errors.New("action request already stored")

// settle stores the terminal state and its audit entries in one commit.
func (r *run) settle(ctx context.Context, as []coreactions.Audit) error {
	rec := *r.rec
	switch st := r.sub.State.(type) {
	case coreactions.Completed:
		rec.State, rec.Refusal = db.ActionCompleted, ""
		rec.Task, rec.Batch, rec.Proposals = st.Result.TaskID, st.Result.BatchID, st.Result.ProposalIDs
	case coreactions.Refused:
		rec.State, rec.Refusal = db.ActionRefused, st.Reason
	default:
		return nil
	}
	var v int64
	if err := r.commit(ctx, as, func(tx *db.Tx) error {
		var err error
		v, err = tx.SaveActionRequest(ctx, rec, rec.Version, r.s.d.Clock())
		return err
	}); err != nil {
		return err
	}
	rec.Version = v
	r.rec = &rec
	return nil
}

// outcome is what the caller is told, from the core state and the stored row.
func (r *run) outcome() Outcome {
	o := Outcome{RequestID: r.sub.Request.RequestID, Message: r.detail}
	if r.dup && r.rec != nil {
		out := outcomeOfRecord(*r.rec)
		out.Replayed = true
		return out
	}
	switch st := r.sub.State.(type) {
	case coreactions.Completed:
		o.State, o.Result, o.Message = db.ActionCompleted, st.Result, completedMessage(r.sub.Request.Kind)
	case coreactions.Refused:
		o.State, o.Refusal = db.ActionRefused, st.Reason
		if o.Message == "" {
			o.Message = message(st.Reason)
		}
	case coreactions.Submitting:
		o.State, o.Message = db.ActionSubmitting, "This request is being finished. It will not be started twice."
	case coreactions.Cancelled:
		o.State, o.Message = db.ActionCancelled, "This request was cancelled."
	default:
		o.Message = "The request is not ready."
	}
	return o
}

// ---- effects ----

// startDispatch runs the effect on a goroutine owned by the service, so a
// caller that goes away after the intent was stored cannot leave it half done.
func (r *run) startDispatch(c coreactions.Dispatch) {
	if r.dup {
		return
	}
	r.done = make(chan struct{})
	r.handed = true
	r.s.g.Go(func() error {
		defer close(r.done)
		defer r.release()
		r.effect(r.s.ctx, c)
		return nil
	})
}

// wait returns the outcome once the effect settled, or the pending state when
// the caller stops waiting first (the effect still finishes in the background).
func (r *run) wait(ctx context.Context) (Outcome, error) {
	select {
	case <-r.done:
		if r.err != nil {
			return Outcome{RequestID: r.sub.Request.RequestID, State: db.ActionSubmitting,
				Message: "This request is being finished. It will not be started twice."}, r.err
		}
		return r.outcome(), nil
	case <-ctx.Done():
		return Outcome{RequestID: r.sub.Request.RequestID, State: db.ActionSubmitting,
			Message: "This request is being finished. It will not be started twice."}, nil
	}
}

// effect rechecks the scope at execution time, performs the one effect the
// core chose and feeds the result back, fenced by attempt.
func (r *run) effect(ctx context.Context, c coreactions.Dispatch) {
	req := c.Request
	if err := r.s.d.Scope.StillTracked(ctx, req.RepoID); err != nil {
		reason := coreactions.RefusePrepareFailed
		if errors.Is(err, db.ErrNotFound) {
			reason = coreactions.RefuseUntracked
		}
		r.feed(ctx, coreactions.SubmitFailed{Attempt: c.Attempt, Outcome: coreactions.SubmitDefiniteRefusal, Reason: reason})
		return
	}
	f, err := r.s.d.Scope.Facts(ctx, req.RepoID)
	if err != nil {
		r.feed(ctx, coreactions.SubmitFailed{Attempt: c.Attempt, Outcome: coreactions.SubmitDefiniteRefusal, Reason: scopeReason(err)})
		return
	}
	if ok, why := offered(f, req); !ok {
		r.feed(ctx, coreactions.SubmitFailed{Attempt: c.Attempt, Outcome: coreactions.SubmitDefiniteRefusal, Reason: why})
		return
	}
	if pin := coreactions.Pin(f, req.Kind); r.expectedPin != "" && pin != r.expectedPin {
		r.feed(ctx, coreactions.SubmitFailed{Attempt: c.Attempt, Outcome: coreactions.SubmitDefiniteRefusal, Reason: coreactions.RefuseStaleHead})
		return
	}
	res, ferr := r.perform(ctx, c)
	if ferr == nil {
		r.feed(ctx, coreactions.Submitted{Attempt: c.Attempt, Result: res})
		return
	}
	r.detail = ferr.message
	r.feed(ctx, coreactions.SubmitFailed{Attempt: c.Attempt, Outcome: ferr.outcome, Reason: ferr.reason})
	if _, still := r.sub.State.(coreactions.Submitting); still {
		r.s.retryLater(req.RequestID)
	}
}

// effectFailure classifies how an effect failed.
type effectFailure struct {
	outcome coreactions.SubmitOutcome
	reason  coreactions.RefuseReason
	message string
}

// reconcile is the read-only lookup of the durable link of a request.
func (r *run) reconcile(ctx context.Context, c coreactions.ReconcileDispatch) {
	res, outcome := r.s.lookup(ctx, r.sub.Request)
	r.feed(ctx, coreactions.Reconciled{Attempt: c.Attempt, Outcome: outcome, Result: res})
}

func (s *Service) lookup(ctx context.Context, req coreactions.Request) (coreactions.Result, coreactions.ReconcileOutcome) {
	switch req.Kind.Effect() {
	case coreactions.EffectStartPull:
		ids, err := s.d.Proposals.FindForRequest(ctx, req.RequestID)
		switch {
		case err == nil && len(ids) > 0:
			return coreactions.Result{ProposalIDs: ids}, coreactions.ReconcileFound
		case err == nil || errors.Is(err, db.ErrNotFound):
			return coreactions.Result{}, coreactions.ReconcileAbsent
		}
		return coreactions.Result{}, coreactions.ReconcileNoAnswer
	}
	id, err := s.d.Tasks.LookupRequest(ctx, req.RequestID)
	switch {
	case err == nil:
		return coreactions.Result{TaskID: id}, coreactions.ReconcileFound
	case errors.Is(err, db.ErrNotFound):
		return coreactions.Result{}, coreactions.ReconcileAbsent
	}
	return coreactions.Result{}, coreactions.ReconcileNoAnswer
}
