package proposals

import (
	"context"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// The InitRepo and PublishRepo executors (NR-01, NR-03, NR-04). They do not do
// the work: the durable lifecycle in internal/newrepos does, in stages, with
// its own receipts, locks and reconciliation. This executor is the proposal's
// side of the hand-off:
//
//   - it writes only after a confirmation (Step emits Execute only then) and
//     only when the operation's immutable spec still equals the proposal's;
//   - it records a durable effect (the hand-off of this attempt) and its audit
//     entry BEFORE it calls the lifecycle, so a second run of the same attempt
//     can never confirm twice;
//   - it returns once the lifecycle owns the operation: the proposal stays
//     Confirmed until the operation reaches an outcome, which comes back
//     through OperationChanged (Ready -> executed, Stopped -> failed);
//   - an unknown outcome (a lost reply, an uncertain stage) is only ever
//     reconciled, read-only, never confirmed again; a human retry is a new
//     attempt and a new confirmation.
//
// No lock is held while calling the lifecycle (it takes the main lock itself),
// and no token, path or host answer appears here: the port carries IDs, a spec
// hash and an attempt number.

const (
	stageNewRepoStarted    = "newrepo_confirm_started"
	stageNewRepoAccepted   = "newrepo_confirm_accepted"
	stageNewRepoRefused    = "newrepo_confirm_refused"
	stageNewRepoUnknown    = "newrepo_confirm_outcome_unknown"
	stageNewRepoNotStarted = "newrepo_not_started"
	stageNewRepoReconcile  = "newrepo_reconcile_asked"
	stageNewRepoLinked     = "newrepo_proposal_linked"
	stageNewRepoSettled    = "newrepo_outcome_followed"
)

const newRepoUnknownMsg = "Yggdrasil could not tell whether the work started or finished, and it will not start it again on its own. Retry checks the state first, read-only, and only starts the work if it certainly has not begun."

// isNewRepoAction reports whether a is an InitRepo or PublishRepo action, and
// the operation it belongs to.
func isNewRepoAction(a coreprop.Action) (core.OperationID, bool) {
	switch v := a.(type) {
	case coreprop.InitRepo:
		return v.Operation, true
	case coreprop.PublishRepo:
		return v.Operation, true
	}
	return "", false
}

func initSpecOf(a coreprop.InitRepo) newrepos.InitSpec {
	return newrepos.InitSpec{Project: a.Project, Repo: a.Repo, Leaf: a.Leaf, Branch: a.Branch,
		README: a.README, Ignore: a.Ignore, CommitMessage: a.CommitMessage}
}

func publishSpecOf(a coreprop.PublishRepo) newrepos.PublishSpec {
	return newrepos.PublishSpec{Repo: a.Repo, HostInstance: a.HostInstance, Owner: a.Owner, OwnerKind: newrepos.OwnerKind(a.OwnerKind),
		Name: a.Name, Visibility: newrepos.Visibility(a.Visibility), Branch: a.Branch, ExpectedHead: a.ExpectedHead, Marker: a.Marker}
}

// operationView reduces the sealed lifecycle state to what a proposal needs.
func operationView(st newrepos.State) coreprop.OperationView {
	flowOf := func(s newrepos.Stage) coreprop.OperationFlow {
		switch s.(type) {
		case newrepos.InitStage:
			return coreprop.FlowInit
		case newrepos.PublishStage:
			return coreprop.FlowPublish
		}
		return coreprop.FlowNone
	}
	switch v := st.(type) {
	case newrepos.Reserved, newrepos.AwaitingConfirm:
		return coreprop.OperationView{Phase: coreprop.OpIdle}
	case newrepos.Initializing:
		return coreprop.OperationView{Phase: coreprop.OpRunning, Flow: coreprop.FlowInit}
	case newrepos.Publishing:
		return coreprop.OperationView{Phase: coreprop.OpRunning, Flow: coreprop.FlowPublish}
	case newrepos.Ready:
		return coreprop.OperationView{Phase: coreprop.OpReady, Head: v.InitialHead}
	case newrepos.Published:
		return coreprop.OperationView{Phase: coreprop.OpPublished, Head: v.Head, URL: v.URL}
	case newrepos.Uncertain:
		return coreprop.OperationView{Phase: coreprop.OpUncertain, Flow: flowOf(v.Stage)}
	case newrepos.Stopped:
		return coreprop.OperationView{Phase: coreprop.OpStopped, Flow: flowOf(v.Stage), StopReason: string(v.Reason)}
	}
	return coreprop.OperationView{} // an unknown state: no plan acts on it
}

// seenAttempts are the attempt numbers the operation has used: its state's and
// its stage effects'. The hand-off record of a proposal (no stage) is not one.
func seenAttempts(rec db.ReservationRecord, effects []db.EffectRecord) []int {
	var out []int
	switch v := rec.Operation.State.(type) {
	case newrepos.Initializing:
		out = append(out, v.Attempt)
	case newrepos.Publishing:
		out = append(out, v.Attempt)
	case newrepos.Uncertain:
		out = append(out, v.Attempt)
	case newrepos.Stopped:
		out = append(out, v.Attempt)
	case newrepos.Reserved, newrepos.AwaitingConfirm, newrepos.Ready, newrepos.Published:
		// No attempt has run in these states.
	}
	for _, e := range effects {
		if e.Stage != "" {
			out = append(out, e.Attempt)
		}
	}
	return out
}

// newRepoTarget is what the executor confirms: the operation and the spec hash
// the lifecycle compares against its own immutable spec.
type newRepoTarget struct {
	kind   core.ProposalKind
	op     core.OperationID
	repo   core.RepoID
	marker string
	hash   string
}

func targetOf(p coreprop.Proposal) (newRepoTarget, bool) {
	switch a := p.Action.(type) {
	case coreprop.InitRepo:
		return newRepoTarget{kind: core.ProposalInitRepo, op: a.Operation, repo: a.Repo, hash: initSpecOf(a).Hash()}, true
	case coreprop.PublishRepo:
		return newRepoTarget{kind: core.ProposalPublish, op: a.Operation, repo: a.Repo, marker: a.Marker, hash: publishSpecOf(a).Hash()}, true
	}
	return newRepoTarget{}, false
}

func stale(msg string) outcome { return failed(coreprop.FailStale, false, "%s", msg) }

// loadOperation reads the operation of a proposal and checks that the
// proposal's immutable action is exactly what the operation holds. A non-nil
// outcome ends the executor with nothing called.
func (s *Service) loadOperation(ctx context.Context, p coreprop.Proposal, t newRepoTarget) (db.ReservationRecord, *outcome) {
	stop := func(o outcome) (db.ReservationRecord, *outcome) { return db.ReservationRecord{}, &o }
	if err := p.Action.Validate(); err != nil || coreprop.Hash(p.Action) != p.ActionHash {
		return stop(failed(coreprop.FailPrecondition, false, "This proposal is not valid any more, so nothing was started. Ask for a new one."))
	}
	rec, err := s.cfg.Store.GetReservation(ctx, t.op)
	if errors.Is(err, db.ErrNotFound) {
		return stop(failed(coreprop.FailPrecondition, false, "The new repo this proposal is for no longer exists, so nothing was started."))
	}
	if err != nil {
		return stop(failed(coreprop.FailExec, true, "Yggdrasil could not read the state of this new repo, so it started nothing. Try again."))
	}
	if rec.Operation.Repo != t.repo {
		return stop(stale("This proposal names another repo than the operation it belongs to, so nothing was started. Ask for a new one."))
	}
	switch a := p.Action.(type) {
	case coreprop.InitRepo:
		spec := initSpecOf(a)
		if rec.SpecHash != spec.Hash() || rec.Operation.Project != a.Project || rec.Leaf != a.Leaf {
			return stop(stale("What you confirmed is not what was reserved for this new repo, so nothing was started. Reject this and start again."))
		}
		if rec.Proposal != p.ID || rec.SetupTask != p.Task {
			return stop(stale("This is not the proposal this new repo is waiting for, so nothing was started."))
		}
	case coreprop.PublishRepo:
		if spec, ok := publishSpecInState(rec.Operation.State); ok && spec.Hash() != t.hash {
			return stop(stale("A different publish is already in progress for this repo, so nothing was started."))
		}
	}
	return rec, nil
}

// publishSpecInState is the publish spec the operation already holds, if any.
func publishSpecInState(st newrepos.State) (newrepos.PublishSpec, bool) {
	var r newrepos.Resumable
	switch v := st.(type) {
	case newrepos.Publishing:
		return v.Spec, true
	case newrepos.Uncertain:
		r = v.Resume
	case newrepos.Stopped:
		r = v.Resume
	case newrepos.Reserved, newrepos.AwaitingConfirm, newrepos.Initializing, newrepos.Ready, newrepos.Published:
		return newrepos.PublishSpec{}, false
	}
	if rp, ok := r.(newrepos.ResumePublish); ok {
		return rp.Spec, true
	}
	return newrepos.PublishSpec{}, false
}

// initTracked is the narrow resolver for a new repo (NR-01): the tracked
// project of the reservation exists, and the repo is still reserved (or, once
// finished, ordinary and tracked). Only the initialization proposal of that
// operation passes through here; every other proposal needs a ready repo.
func (s *Service) initTracked(ctx context.Context, a coreprop.InitRepo) bool {
	if _, err := s.cfg.Store.GetProject(ctx, a.Project); err != nil {
		return false
	}
	if r, err := s.cfg.Store.GetReservedRepo(ctx, a.Repo); err == nil {
		return r.ProjectID == a.Project
	}
	return s.repoTracked(ctx, a.Repo)
}

// proposalTracked is repoTracked for a proposal: an initialization proposal
// acts on a reserved repo, which ordinary lookups never show.
func (s *Service) proposalTracked(ctx context.Context, p coreprop.Proposal) bool {
	if a, ok := p.Action.(coreprop.InitRepo); ok {
		return s.initTracked(ctx, a)
	}
	return s.repoTracked(ctx, p.Repo)
}

// execNewRepo runs one confirmed attempt of an InitRepo or PublishRepo.
func (s *Service) execNewRepo(ctx context.Context, p coreprop.Proposal, attempt int) outcome {
	t, ok := targetOf(p)
	if !ok {
		return failed(coreprop.FailPrecondition, false, "This kind of proposal cannot run yet.")
	}
	if s.cfg.NewRepos == nil {
		return failed(coreprop.FailPrecondition, false, "Creating and publishing repos is not set up in this build of Yggdrasil, so nothing was started.")
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()
	rec, stop := s.loadOperation(ctx, p, t)
	if stop != nil {
		return *stop
	}
	view := operationView(rec.Operation.State)
	switch coreprop.PlanNewRepo(t.kind, view) {
	case coreprop.PlanNewRepoDone:
		return s.follow(ctx, p, t, view, followAfterCall)
	case coreprop.PlanNewRepoWait:
		return outcome{pending: true}
	case coreprop.PlanNewRepoReconcile:
		return s.reconcileThenFollow(ctx, p, t, attempt)
	case coreprop.PlanNewRepoStart:
		return s.startNewRepo(ctx, p, t, rec, attempt)
	}
	// Refused: say why in the operation's own words when it has them.
	if v := coreprop.JudgeOperation(t.kind, view); v.Kind == coreprop.VerdictFailed {
		return failed(v.FailKind, false, "%s", v.Message)
	}
	return failed(coreprop.FailPrecondition, false, "The repo is not in a state where this can run, so nothing was started. Check the repo, then ask for a new proposal.")
}

// followMode says what a verdict that is not final means.
type followMode int

const (
	// followAfterCall: the lifecycle accepted the call (or nothing needed one);
	// a running or uncertain operation is the lifecycle's to finish.
	followAfterCall followMode = iota
	// followAfterReconcile: a read-only reconciliation just ran.
	followAfterReconcile
)

// follow turns the operation's state into the executor's outcome.
func (s *Service) follow(ctx context.Context, p coreprop.Proposal, t newRepoTarget, view coreprop.OperationView, mode followMode) outcome {
	v := coreprop.JudgeOperation(t.kind, view)
	switch v.Kind {
	case coreprop.VerdictDone:
		return done(v.Result)
	case coreprop.VerdictFailed:
		return failed(v.FailKind, v.Retryable, "%s", v.Message)
	case coreprop.VerdictPending:
		return outcome{pending: true}
	case coreprop.VerdictUnknown:
		if mode == followAfterCall {
			return outcome{pending: true} // the lifecycle reconciles on its own and reports back
		}
		return failed(coreprop.FailInterrupted, true, "%s", newRepoUnknownMsg)
	}
	// Not started.
	s.settleNotStarted(ctx, p)
	return failed(coreprop.FailInterrupted, true, "The work did not start, so nothing was changed. Retry to start it.")
}

// settleNotStarted marks the hand-off effects of this proposal that the
// operation proves never reached the lifecycle as failed (they did not happen).
func (s *Service) settleNotStarted(ctx context.Context, p coreprop.Proposal) {
	effects, err := s.cfg.Store.ListEffectsByProposal(ctx, p.ID)
	if err != nil {
		return
	}
	for _, e := range openEffects(effects) {
		if e.Stage == "" {
			if err := s.settleEffect(ctx, p, e, db.EffectFailed, "", nil, "", stageNewRepoNotStarted); err != nil {
				s.cfg.Logger.Error("could not record that a hand-off never started", "proposal", p.ID, "error", s.redact(err.Error()))
			}
		}
	}
}

// beginHandoff records the intent of this attempt's hand-off and its audit
// entry in one transaction, BEFORE the lifecycle is called. A second intent for
// the same attempt is errEffectExists.
func (s *Service) beginHandoff(ctx context.Context, p coreprop.Proposal, t newRepoTarget, attempt int) error {
	now := s.now()
	err := s.cfg.Store.Commit(ctx, func(tx *db.Tx) error {
		if err := tx.InsertEffect(ctx, db.EffectRecord{
			Proposal: p.ID, Operation: t.op, Attempt: attempt, State: db.EffectIntent, Marker: t.marker, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return s.stageEntry(ctx, tx, p, attempt, stageNewRepoStarted, now)
	})
	if errors.Is(err, db.ErrConflict) {
		return errEffectExists
	}
	return err
}

func (s *Service) startNewRepo(ctx context.Context, p coreprop.Proposal, t newRepoTarget, rec db.ReservationRecord, attempt int) outcome {
	if !s.proposalTracked(ctx, p) {
		return failed(coreprop.FailRepoUntracked, false, "The project or repo is no longer tracked, so nothing was started. Track it again, then ask for a new proposal.")
	}
	effects, err := s.cfg.Store.ListEffectsByOperation(ctx, t.op)
	if err != nil {
		return failed(coreprop.FailExec, true, "Yggdrasil could not read its record of this work, so it started nothing. Try again.")
	}
	fresh := coreprop.FreshAttempt(attempt, seenAttempts(rec, effects))
	if err := s.beginHandoff(ctx, p, t, attempt); err != nil {
		if errors.Is(err, errEffectExists) {
			return failed(coreprop.FailInterrupted, true, "An earlier run of this attempt did not finish. Nothing was started again. Retry checks the state first.")
		}
		return failed(coreprop.FailExec, true, "Yggdrasil could not record that it is about to start, so it started nothing. Try again.")
	}
	eff, err := s.cfg.Store.GetEffectByKey(ctx, p.ID, "", attempt)
	if err != nil {
		return failed(coreprop.FailInterrupted, true, "Yggdrasil lost its record of this hand-off before starting. Nothing was started. Retry checks the state first.")
	}
	if t.kind == core.ProposalInitRepo {
		err = s.cfg.NewRepos.ConfirmInit(ctx, t.op, t.hash, fresh)
	} else {
		err = s.cfg.NewRepos.ConfirmPublish(ctx, t.op, t.hash, fresh)
	}
	if err == nil {
		s.auditStageBestEffort(ctx, p, attempt, stageNewRepoAccepted)
		return s.followCurrent(ctx, p, t, followAfterCall)
	}
	if definiteRefusal(err) {
		if serr := s.settleEffect(ctx, p, eff, db.EffectFailed, "", nil, "", stageNewRepoRefused); serr != nil {
			s.cfg.Logger.Error("could not record a refused hand-off", "proposal", p.ID, "error", s.redact(serr.Error()))
		}
		return failed(coreprop.FailPrecondition, true, "%s", userText(err, "The new-repo service refused to start this, and nothing was changed. Check the repo, then retry."))
	}
	// Unknown: never confirm again for this attempt. Mark it, then look once,
	// read-only.
	if serr := s.settleEffect(ctx, p, eff, db.EffectUnknown, "", nil, "", stageNewRepoUnknown); serr != nil {
		s.cfg.Logger.Error("could not record an unknown hand-off", "proposal", p.ID, "error", s.redact(serr.Error()))
	}
	if ctx.Err() != nil {
		return failed(coreprop.FailInterrupted, true, "%s", newRepoUnknownMsg)
	}
	return s.reconcileThenFollow(ctx, p, t, attempt)
}

// followCurrent re-reads the operation and follows it.
func (s *Service) followCurrent(ctx context.Context, p coreprop.Proposal, t newRepoTarget, mode followMode) outcome {
	rec, err := s.cfg.Store.GetReservation(ctx, t.op)
	if err != nil {
		return failed(coreprop.FailInterrupted, true, "%s", newRepoUnknownMsg)
	}
	return s.follow(ctx, p, t, operationView(rec.Operation.State), mode)
}

// reconcileThenFollow asks the lifecycle for its read-only reconciliation of
// the operation (never a confirm) and follows the resulting state.
func (s *Service) reconcileThenFollow(ctx context.Context, p coreprop.Proposal, t newRepoTarget, attempt int) outcome {
	s.auditStageBestEffort(ctx, p, attempt, stageNewRepoReconcile)
	if err := s.cfg.NewRepos.ReconcileOperation(ctx, t.op); err != nil {
		s.cfg.Logger.Error("could not reconcile a new-repo operation", "proposal", p.ID, "error", s.redact(err.Error()))
	}
	return s.followCurrent(ctx, p, t, followAfterReconcile)
}

// reconcileNewRepo judges, after a crash, an InitRepo or PublishRepo that was
// left Confirmed: read-only, it never calls a confirm. keep is true when a
// worker still owns the operation: the proposal then stays Confirmed and the
// outcome arrives through OperationChanged.
func (s *Service) reconcileNewRepo(ctx context.Context, p coreprop.Proposal) (out coreprop.ReconcileOutcome, res coreprop.Result, keep bool) {
	t, ok := targetOf(p)
	if !ok || s.cfg.NewRepos == nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}, false
	}
	rec, stop := s.loadOperation(ctx, p, t)
	if stop != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}, false
	}
	view := operationView(rec.Operation.State)
	if view.Phase == coreprop.OpRunning || view.Phase == coreprop.OpUncertain {
		if err := s.cfg.NewRepos.ReconcileOperation(ctx, t.op); err != nil {
			s.cfg.Logger.Error("could not reconcile a new-repo operation", "proposal", p.ID, "error", s.redact(err.Error()))
		}
		if rec, err := s.cfg.Store.GetReservation(ctx, t.op); err == nil {
			view = operationView(rec.Operation.State)
		}
	}
	v := coreprop.JudgeOperation(t.kind, view)
	if v.Kind == coreprop.VerdictNotStarted {
		s.settleNotStarted(ctx, p)
	}
	if v.Kind == coreprop.VerdictPending {
		return coreprop.ReconcileUnknown, coreprop.Result{}, true
	}
	out, res = coreprop.ReconcileOf(v)
	return out, res, false
}

// OperationChanged tells the service that a new-repo operation moved (the
// lifecycle reports every state change through the proposals hub wiring). Each
// Confirmed InitRepo or PublishRepo proposal of the operation follows the
// outcome: Ready or Published executes it, a definite stop fails it. A running
// or uncertain operation changes nothing: an unknown outcome is settled by the
// lifecycle's own reconciliation, never by a guess here. It is safe to call
// any number of times; a decided proposal ignores it.
func (s *Service) OperationChanged(ctx context.Context, op core.OperationID) error {
	if err := op.Validate(); err != nil {
		return fmt.Errorf("operation changed: %w", db.ErrNotFound)
	}
	rec, err := s.cfg.Store.GetReservation(ctx, op)
	if err != nil {
		return fmt.Errorf("operation changed %s: %w", op, err)
	}
	effects, err := s.cfg.Store.ListEffectsByOperation(ctx, op)
	if err != nil {
		return fmt.Errorf("operation changed %s: %w", op, err)
	}
	ids := map[core.ProposalID]bool{}
	if rec.Proposal != "" {
		ids[rec.Proposal] = true
	}
	for _, e := range effects {
		if e.Stage == "" {
			ids[e.Proposal] = true
		}
	}
	view := operationView(rec.Operation.State)
	var firstErr error
	for id := range ids {
		prop, err := s.cfg.Store.GetProposalRecord(ctx, id)
		if err != nil {
			firstErr = errors.Join(firstErr, fmt.Errorf("load proposal %s: %w", id, err))
			continue
		}
		st, confirmed := prop.Proposal.State.(coreprop.Confirmed)
		pop, isNR := isNewRepoAction(prop.Proposal.Action)
		if !confirmed || !isNR || pop != op {
			continue
		}
		v := coreprop.JudgeOperation(prop.Proposal.Action.Kind(), view)
		var in coreprop.Input
		switch v.Kind {
		case coreprop.VerdictDone:
			in = coreprop.ExecDone{Attempt: st.Attempt, Result: v.Result, Now: s.now()}
		case coreprop.VerdictFailed:
			in = coreprop.ExecFailed{Attempt: st.Attempt, Kind: v.FailKind, Message: s.redact(v.Message), Retryable: v.Retryable, Now: s.now()}
		default:
			continue
		}
		s.auditStageBestEffort(ctx, prop.Proposal, st.Attempt, stageNewRepoSettled)
		if err := s.applyInput(ctx, id, in); err != nil {
			firstErr = errors.Join(firstErr, err)
		}
	}
	return firstErr
}
