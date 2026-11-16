package newrepos

import (
	"crypto/sha256"
	"fmt"
)

// Step is (operation2, commands) = Step(operation1, input), the transition table
// of docs/design/phase-6-newrepos.md. It is pure: no clock, IDs, randomness or
// logging; times and IDs arrive on the input and commands are data.
//
// Conventions:
//   - Any (state, input) pair the table does not list returns the operation
//     unchanged and no commands. A guard that fails (wrong hash, stale attempt,
//     wrong stage) does the same, except where the user needs to be told why
//     (Reserve refusals, PublishConfirmed facts, a retry whose preconditions
//     changed): then the operation is unchanged and the one command is Explain.
//   - Command lists are ordered: records (PersistReservationAndSetupDraft,
//     PersistIntent, PersistReceipt, PersistFailure, Audit) first, then
//     presentation (PublishPreview, Explain), then effects (RunInitStage,
//     RunPublishStage, ReconcileStage, CancelWorker). The shell commits the
//     records in one transaction before it runs any effect, so an intent is
//     always durable before its effect.
//   - Operation.Version belongs to the shell (optimistic concurrency of the
//     stored row). Step never changes it; the shell bumps it in Store.Commit.
//   - A stage result carries the Attempt and Stage it answers. A result for any
//     other attempt or stage is ignored (fencing). The shell supplies strictly
//     increasing attempt numbers for Confirmed, PublishConfirmed and
//     RetryConfirmed, and a retry must exceed the attempt that stopped or
//     became uncertain (Stopped.Attempt, Uncertain.Attempt).
//   - A receipt of the wrong sealed variant, or one that does not match the
//     confirmed spec, is an impossible input (die class): the operation Stops
//     with StopConflict, the worker is cancelled and no further stage runs.
func Step(o Operation, in Input) (Operation, []Command) {
	switch v := in.(type) {
	case Reserve:
		return stepReserve(o, v)
	case ProposalCreated:
		return stepProposalCreated(o, v)
	case Confirmed:
		return stepConfirmed(o, v)
	case StageSucceeded:
		return stepSucceeded(o, v)
	case StageFailed:
		return stepFailed(o, v)
	case PublishConfirmed:
		return stepPublishConfirmed(o, v)
	case Recovered:
		return stepRecovered(o)
	case Reconciled:
		return stepReconciled(o, v)
	case RetryConfirmed:
		return stepRetry(o, v)
	case Reject:
		return stepReject(o)
	case Untracked:
		return stepUntracked(o)
	default:
		return o, nil
	}
}

// Reasons recorded on Uncertain.
const (
	reasonUnknownOutcome = "outcome_unknown"
	reasonRestarted      = "restarted"
)

func with(o Operation, st State) Operation {
	o.State = st
	return o
}

func stageName(s Stage) string {
	if s == nil {
		return ""
	}
	return s.Name()
}

func record(o Operation, action string, stage Stage, outcome string) Audit {
	return Audit{Action: action, Operation: o.ID, Stage: stageName(stage), Outcome: outcome}
}

// withReceipt returns a new slice (never aliasing done) ending in extra.
func withReceipt(done []Receipt, extra ...Receipt) []Receipt {
	out := make([]Receipt, 0, len(done)+len(extra))
	out = append(out, done...)
	return append(out, extra...)
}

func initIndex(s InitStage) int {
	for i, x := range InitStages() {
		if x == s {
			return i
		}
	}
	return -1
}

func publishIndex(s PublishStage) int {
	for i, x := range PublishStages() {
		if x == s {
			return i
		}
	}
	return -1
}

func fileHash(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func remoteOf(done []Receipt) (RemoteReceipt, bool) {
	for _, r := range done {
		if v, ok := r.(RemoteReceipt); ok {
			return v, true
		}
	}
	return RemoteReceipt{}, false
}

func commitOf(done []Receipt) (CommitReceipt, bool) {
	for _, r := range done {
		if v, ok := r.(CommitReceipt); ok {
			return v, true
		}
	}
	return CommitReceipt{}, false
}

// initReceiptOK: the receipt is the sealed variant of the stage and, for a
// file, hashes exactly the confirmed content.
func initReceiptOK(spec InitSpec, stage InitStage, rc Receipt) bool {
	if ValidateReceiptForStage(stage, rc) != nil {
		return false
	}
	if f, ok := rc.(FileReceipt); ok {
		content := spec.README
		if stage == StageWriteIgnore {
			content = spec.Ignore
		}
		return f.ContentHash == fileHash(content)
	}
	return true
}

// publishReceiptOK: the receipt is the variant of the stage and belongs to the
// confirmed destination and head; origin must name the remote this operation
// created, and the push must hold exactly the confirmed head.
func publishReceiptOK(spec PublishSpec, stage PublishStage, done []Receipt, rc Receipt) bool {
	if ValidateReceiptForStage(stage, rc) != nil {
		return false
	}
	switch v := rc.(type) {
	case RemoteReceipt:
		return spec.ValidateDestination(v.URL) == nil
	case OriginReceipt:
		r, ok := remoteOf(done)
		return ok && r.URL == v.URL
	case PushReceipt:
		return v.Head == spec.ExpectedHead
	case DirReceipt, GitReceipt, FileReceipt, CommitReceipt, TrackedReceipt:
		return false
	default:
		return false
	}
}

// refuse leaves the operation unchanged and tells the user why.
func refuse(o Operation, reason StopReason) (Operation, []Command) {
	return o, []Command{Explain{Reason: reason}}
}

// stop moves to Stopped. A die stop (an impossible input) also cancels the worker.
func stop(o Operation, attempt int, stage Stage, reason StopReason, resume Resumable, die bool) (Operation, []Command) {
	cmds := []Command{
		PersistFailure{Operation: o.ID, Stage: stage, Reason: string(reason)},
		record(o, "newrepo.stopped", stage, string(reason)),
		Explain{Reason: reason},
	}
	if die {
		cmds = append(cmds, CancelWorker{Operation: o.ID})
	}
	return with(o, Stopped{Stage: stage, Reason: reason, Resume: resume, Attempt: attempt}), cmds
}

// uncertain moves to Uncertain and asks for the read-only check. It never runs a stage.
func uncertain(o Operation, attempt int, stage Stage, reason string, resume Resumable) (Operation, []Command) {
	return with(o, Uncertain{Attempt: attempt, Stage: stage, Reason: reason, Resume: resume}), []Command{
		PersistFailure{Operation: o.ID, Stage: stage, Reason: reason},
		record(o, "newrepo.uncertain", stage, reason),
		ReconcileStage{Operation: o.ID, Attempt: attempt, Stage: stage},
	}
}

// --- reserve and confirm ---

func stepReserve(o Operation, in Reserve) (Operation, []Command) {
	if o.State != nil {
		return o, nil
	}
	ids := in.IDs
	switch {
	case in.Spec.Validate() != nil,
		ids.Operation.Validate() != nil, ids.Repo.Validate() != nil,
		ids.SetupTask.Validate() != nil, ids.Proposal.Validate() != nil,
		ids.Repo != in.Spec.Repo,
		o.ID != "" && o.ID != ids.Operation,
		o.Project != "" && o.Project != in.Spec.Project,
		o.Repo != "" && o.Repo != ids.Repo:
		return refuse(o, StopPreconditions)
	case !in.ProjectTracked:
		return refuse(o, StopUntracked)
	case !in.LeafFree:
		return refuse(o, StopPathExists)
	case !in.HasAuthor:
		return refuse(o, StopMissingAuthor)
	}
	o.ID, o.Project, o.Repo = ids.Operation, in.Spec.Project, ids.Repo
	return with(o, Reserved{Spec: in.Spec, Hash: in.Spec.Hash()}), []Command{
		PersistReservationAndSetupDraft{Operation: o.ID, Spec: in.Spec, IDs: ids},
		record(o, "newrepo.reserve", nil, "reserved"),
	}
}

func stepProposalCreated(o Operation, in ProposalCreated) (Operation, []Command) {
	st, ok := o.State.(Reserved)
	if !ok || in.ID.Validate() != nil || in.Hash != st.Hash || st.Hash != st.Spec.Hash() {
		return o, nil
	}
	return with(o, AwaitingConfirm{Spec: st.Spec, Proposal: in.ID, Hash: st.Hash}), []Command{
		record(o, "newrepo.proposal", nil, "awaiting_confirm"),
		PublishPreview{Hash: st.Hash},
	}
}

func stepConfirmed(o Operation, in Confirmed) (Operation, []Command) {
	st, ok := o.State.(AwaitingConfirm)
	if !ok || in.Attempt < 1 || in.Hash != st.Hash || st.Hash != st.Spec.Hash() {
		return o, nil
	}
	return beginInit(o, st.Spec, in.Attempt, StageCreateDirectory, nil, "newrepo.confirm")
}

// beginInit starts Attempt at stage with the receipts before it. The intent is
// recorded before the effect command in the same list.
func beginInit(o Operation, spec InitSpec, attempt int, stage InitStage, done []Receipt, action string) (Operation, []Command) {
	done = withReceipt(done)
	return with(o, Initializing{Spec: spec, Attempt: attempt, Stage: stage, Done: done}), []Command{
		PersistIntent{Operation: o.ID, Attempt: attempt, Stage: stage},
		record(o, action, stage, "intent"),
		RunInitStage{Operation: o.ID, Attempt: attempt, Stage: stage, Spec: spec, Done: withReceipt(done)},
	}
}

func beginPublish(o Operation, spec PublishSpec, attempt int, stage PublishStage, done []Receipt, action string) (Operation, []Command) {
	done = withReceipt(done)
	return with(o, Publishing{Spec: spec, Attempt: attempt, Stage: stage, Done: done}), []Command{
		PersistIntent{Operation: o.ID, Attempt: attempt, Stage: stage},
		record(o, action, stage, "intent"),
		RunPublishStage{Operation: o.ID, Attempt: attempt, Stage: stage, Spec: spec, Done: withReceipt(done)},
	}
}

// --- stage results ---

func stepSucceeded(o Operation, in StageSucceeded) (Operation, []Command) {
	v := describe(o.State)
	if v.kind != kindRunning || in.Attempt != v.attempt || in.Stage != v.stage {
		return o, nil
	}
	return advance(o, v, in.Receipt, "newrepo.stage_done")
}

// advance records a receipt for the view's stage, whichever flow it belongs to.
func advance(o Operation, v view, rc Receipt, action string) (Operation, []Command) {
	switch r := v.resume.(type) {
	case ResumeInit:
		if s, ok := v.stage.(InitStage); ok {
			return advanceInit(o, r.Spec, v.attempt, s, r.Done, rc, action)
		}
	case ResumePublish:
		if s, ok := v.stage.(PublishStage); ok {
			return advancePublish(o, r.Spec, v.attempt, s, r.Done, rc, action)
		}
	case ResumeNone:
	default:
	}
	return o, nil
}

// advanceInit records the receipt of stage and starts the next one, or finishes.
func advanceInit(o Operation, spec InitSpec, attempt int, stage InitStage, done []Receipt, rc Receipt, action string) (Operation, []Command) {
	idx := initIndex(stage)
	if idx < 0 || len(done) != idx {
		return o, nil // malformed state; the boundary rejects it before Step
	}
	resume := ResumeInit{Spec: spec, Done: withReceipt(done)}
	if !initReceiptOK(spec, stage, rc) {
		return stop(o, attempt, stage, StopConflict, resume, true)
	}
	all := withReceipt(done, rc)
	records := []Command{
		PersistReceipt{Operation: o.ID, Attempt: attempt, Stage: stage, Receipt: rc},
		record(o, action, stage, "ok"),
	}
	if idx == len(InitStages())-1 {
		c, ok := commitOf(all)
		if !ok {
			return stop(o, attempt, stage, StopConflict, resume, true)
		}
		return with(o, Ready{Repo: spec.Repo, InitialHead: c.Commit}), append(records, record(o, "newrepo.ready", stage, "ready"))
	}
	next := InitStages()[idx+1]
	return with(o, Initializing{Spec: spec, Attempt: attempt, Stage: next, Done: all}),
		append(records, RunInitStage{Operation: o.ID, Attempt: attempt, Stage: next, Spec: spec, Done: withReceipt(all)})
}

// advancePublish is advanceInit for the publish stages. The remote receipt
// from CreateRemote stays in Done and travels to AddOrigin and PushHead.
func advancePublish(o Operation, spec PublishSpec, attempt int, stage PublishStage, done []Receipt, rc Receipt, action string) (Operation, []Command) {
	idx := publishIndex(stage)
	if idx < 0 || len(done) != idx {
		return o, nil
	}
	resume := ResumePublish{Spec: spec, Done: withReceipt(done)}
	if !publishReceiptOK(spec, stage, done, rc) {
		return stop(o, attempt, stage, StopConflict, resume, true)
	}
	all := withReceipt(done, rc)
	records := []Command{
		PersistReceipt{Operation: o.ID, Attempt: attempt, Stage: stage, Receipt: rc},
		record(o, action, stage, "ok"),
	}
	if idx == len(PublishStages())-1 {
		r, ok := remoteOf(all)
		if !ok {
			return stop(o, attempt, stage, StopConflict, resume, true)
		}
		return with(o, Published{RemoteID: r.RemoteID, URL: r.URL, Head: spec.ExpectedHead}),
			append(records, record(o, "newrepo.published", stage, "published"))
	}
	next := PublishStages()[idx+1]
	return with(o, Publishing{Spec: spec, Attempt: attempt, Stage: next, Done: all}),
		append(records, RunPublishStage{Operation: o.ID, Attempt: attempt, Stage: next, Spec: spec, Done: withReceipt(all)})
}

func stepFailed(o Operation, in StageFailed) (Operation, []Command) {
	if in.Outcome != OutcomeDefiniteFailure && in.Outcome != OutcomeUnknown {
		return o, nil
	}
	v := describe(o.State)
	if v.kind != kindRunning || in.Attempt != v.attempt || in.Stage != v.stage {
		return o, nil
	}
	if in.Outcome == OutcomeUnknown {
		return uncertain(o, v.attempt, v.stage, reasonUnknownOutcome, v.resume)
	}
	reason := in.Reason
	if reason == "" || reason.Validate() != nil {
		reason = StopStageFailed
	}
	return stop(o, v.attempt, v.stage, reason, v.resume, false)
}

func stepRecovered(o Operation) (Operation, []Command) {
	v := describe(o.State)
	if v.kind != kindRunning {
		return o, nil
	}
	return uncertain(o, v.attempt, v.stage, reasonRestarted, v.resume)
}

// stepReconciled handles the answer of the read-only check. Only an exact
// success moves forward; nothing here ever repeats the uncertain stage.
func stepReconciled(o Operation, in Reconciled) (Operation, []Command) {
	v := describe(o.State)
	if v.kind != kindUncertain || in.Attempt != v.attempt || in.Stage != v.stage {
		return o, nil
	}
	switch in.Outcome {
	case ReconcileExactSuccess:
		return advance(o, v, in.Receipt, "newrepo.reconciled")
	case ReconcileAbsent:
		// The effect is not there: the stage did not run. A retry needs a fresh confirmation.
		return stop(o, v.attempt, v.stage, StopStageFailed, v.resume, false)
	case ReconcileConflict:
		return stop(o, v.attempt, v.stage, StopConflict, v.resume, false)
	case ReconcileUnknown:
		return o, nil // stay Uncertain, write nothing
	default:
		return o, nil
	}
}

// --- retry ---

// retryable: a rejected or untracked operation never resumes; everything else
// needs a fresh human confirmation with the original preconditions.
func retryable(r StopReason) bool { return r != StopRejected && r != StopUntracked }

func stepRetry(o Operation, in RetryConfirmed) (Operation, []Command) {
	v := describe(o.State)
	if v.kind != kindUncertain && (v.kind != kindStopped || !retryable(v.reason)) {
		return o, nil
	}
	if in.Attempt < v.attempt+1 || in.Attempt < 1 {
		return o, nil
	}
	switch r := v.resume.(type) {
	case ResumeInit:
		s, ok := v.stage.(InitStage)
		if !ok || initIndex(s) != len(r.Done) || in.Hash != r.Spec.Hash() {
			return o, nil
		}
		if !in.PreconditionsHold {
			return refuse(o, StopPreconditions)
		}
		return beginInit(o, r.Spec, in.Attempt, s, r.Done, "newrepo.retry")
	case ResumePublish:
		s, ok := v.stage.(PublishStage)
		if !ok || publishIndex(s) != len(r.Done) || in.Hash != r.Spec.Hash() {
			return o, nil
		}
		if !in.PreconditionsHold {
			return refuse(o, StopPreconditions)
		}
		return beginPublish(o, r.Spec, in.Attempt, s, r.Done, "newrepo.retry")
	case ResumeNone:
		return o, nil
	default:
		return o, nil
	}
}

// --- publish ---

func stepPublishConfirmed(o Operation, in PublishConfirmed) (Operation, []Command) {
	st, ok := o.State.(Ready)
	spec := in.Spec
	// The visibility is whatever the user confirmed inside the hashed spec;
	// Validate rejects the zero value and the hash binds the rest.
	if !ok || in.Attempt < 1 || spec.Validate() != nil || in.Hash != spec.Hash() || spec.Repo != st.Repo {
		return o, nil
	}
	if !in.OriginAbsent {
		return refuse(o, StopOriginExists)
	}
	if !in.HeadMatches {
		return refuse(o, StopPreconditions)
	}
	return beginPublish(o, spec, in.Attempt, StageCreateRemote, nil, "newrepo.publish_confirm")
}

// --- reject and untracked ---

func stepReject(o Operation) (Operation, []Command) {
	_, reserved := o.State.(Reserved)
	_, awaiting := o.State.(AwaitingConfirm)
	if !reserved && !awaiting {
		return o, nil
	}
	return with(o, Stopped{Reason: StopRejected, Resume: ResumeNone{}}), []Command{
		PersistFailure{Operation: o.ID, Reason: string(StopRejected)},
		record(o, "newrepo.reject", nil, string(StopRejected)),
	}
}

func stepUntracked(o Operation) (Operation, []Command) {
	v := describe(o.State)
	if v.kind != kindIdle && v.kind != kindRunning && v.kind != kindUncertain {
		return o, nil
	}
	return with(o, Stopped{Stage: v.stage, Reason: StopUntracked, Resume: v.resume, Attempt: v.attempt}), []Command{
		PersistFailure{Operation: o.ID, Stage: v.stage, Reason: string(StopUntracked)},
		record(o, "newrepo.untracked", v.stage, string(StopUntracked)),
		CancelWorker{Operation: o.ID},
	}
}

type viewKind int

const (
	kindNone      viewKind = iota // no operation yet
	kindIdle                      // Reserved, AwaitingConfirm, Ready: nothing in flight
	kindRunning                   // Initializing, Publishing
	kindUncertain                 // Uncertain
	kindStopped                   // Stopped
	kindFinal                     // Published
)

// view is the part of a State the generic transitions need: where the stage in
// flight (or parked) stands and what a retry or reconciliation resumes from.
type view struct {
	kind    viewKind
	attempt int
	stage   Stage
	resume  Resumable
	reason  StopReason
}

// describe is the one place that lists every State variant.
func describe(st State) view {
	switch v := st.(type) {
	case nil:
		return view{kind: kindNone}
	case Reserved, AwaitingConfirm, Ready:
		return view{kind: kindIdle, resume: ResumeNone{}}
	case Initializing:
		return view{kind: kindRunning, attempt: v.Attempt, stage: v.Stage, resume: ResumeInit{Spec: v.Spec, Done: withReceipt(v.Done)}}
	case Publishing:
		return view{kind: kindRunning, attempt: v.Attempt, stage: v.Stage, resume: ResumePublish{Spec: v.Spec, Done: withReceipt(v.Done)}}
	case Uncertain:
		return view{kind: kindUncertain, attempt: v.Attempt, stage: v.Stage, resume: v.Resume}
	case Stopped:
		return view{kind: kindStopped, attempt: v.Attempt, stage: v.Stage, resume: v.Resume, reason: v.Reason}
	case Published:
		return view{kind: kindFinal}
	default:
		return view{kind: kindNone}
	}
}
