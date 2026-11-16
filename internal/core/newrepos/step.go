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
//     RetryConfirmed; an Uncertain retry must also exceed the uncertain attempt.
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
	default:
		return false
	}
}

// refuse leaves the operation unchanged and tells the user why.
func refuse(o Operation, reason StopReason) (Operation, []Command) {
	return o, []Command{Explain{Reason: reason}}
}

// stop moves to Stopped. A die stop (an impossible input) also cancels the worker.
func stop(o Operation, stage Stage, reason StopReason, resume Resumable, die bool) (Operation, []Command) {
	cmds := []Command{
		PersistFailure{Operation: o.ID, Stage: stage, Reason: string(reason)},
		record(o, "newrepo.stopped", stage, string(reason)),
		Explain{Reason: reason},
	}
	if die {
		cmds = append(cmds, CancelWorker{Operation: o.ID})
	}
	return with(o, Stopped{Stage: stage, Reason: reason, Resume: resume}), cmds
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
	switch st := o.State.(type) {
	case Initializing:
		if in.Attempt != st.Attempt || in.Stage != Stage(st.Stage) {
			return o, nil
		}
		return advanceInit(o, st.Spec, st.Attempt, st.Stage, st.Done, in.Receipt, "newrepo.stage_done")
	case Publishing:
		if in.Attempt != st.Attempt || in.Stage != Stage(st.Stage) {
			return o, nil
		}
		return advancePublish(o, st.Spec, st.Attempt, st.Stage, st.Done, in.Receipt, "newrepo.stage_done")
	default:
		return o, nil
	}
}

// advanceInit records the receipt of stage and starts the next one, or finishes.
func advanceInit(o Operation, spec InitSpec, attempt int, stage InitStage, done []Receipt, rc Receipt, action string) (Operation, []Command) {
	idx := initIndex(stage)
	if idx < 0 || len(done) != idx {
		return o, nil // malformed state; the boundary rejects it before Step
	}
	resume := ResumeInit{Spec: spec, Done: withReceipt(done)}
	if !initReceiptOK(spec, stage, rc) {
		return stop(o, stage, StopConflict, resume, true)
	}
	all := withReceipt(done, rc)
	records := []Command{
		PersistReceipt{Operation: o.ID, Attempt: attempt, Stage: stage, Receipt: rc},
		record(o, action, stage, "ok"),
	}
	if idx == len(InitStages())-1 {
		c, ok := commitOf(all)
		if !ok {
			return stop(o, stage, StopConflict, resume, true)
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
		return stop(o, stage, StopConflict, resume, true)
	}
	all := withReceipt(done, rc)
	records := []Command{
		PersistReceipt{Operation: o.ID, Attempt: attempt, Stage: stage, Receipt: rc},
		record(o, action, stage, "ok"),
	}
	if idx == len(PublishStages())-1 {
		r, ok := remoteOf(all)
		if !ok {
			return stop(o, stage, StopConflict, resume, true)
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
	var (
		attempt int
		stage   Stage
		resume  Resumable
	)
	switch st := o.State.(type) {
	case Initializing:
		attempt, stage, resume = st.Attempt, st.Stage, ResumeInit{Spec: st.Spec, Done: withReceipt(st.Done)}
	case Publishing:
		attempt, stage, resume = st.Attempt, st.Stage, ResumePublish{Spec: st.Spec, Done: withReceipt(st.Done)}
	default:
		return o, nil
	}
	if in.Attempt != attempt || in.Stage != stage {
		return o, nil
	}
	if in.Outcome == OutcomeUnknown {
		return uncertain(o, attempt, stage, reasonUnknownOutcome, resume)
	}
	reason := in.Reason
	if reason == "" || reason.Validate() != nil {
		reason = StopStageFailed
	}
	return stop(o, stage, reason, resume, false)
}

func stepRecovered(o Operation) (Operation, []Command) {
	switch st := o.State.(type) {
	case Initializing:
		return uncertain(o, st.Attempt, st.Stage, reasonRestarted, ResumeInit{Spec: st.Spec, Done: withReceipt(st.Done)})
	case Publishing:
		return uncertain(o, st.Attempt, st.Stage, reasonRestarted, ResumePublish{Spec: st.Spec, Done: withReceipt(st.Done)})
	default:
		return o, nil
	}
}

// stepReconciled handles the answer of the read-only check. Only an exact
// success moves forward; nothing here ever repeats the uncertain stage.
func stepReconciled(o Operation, in Reconciled) (Operation, []Command) {
	st, ok := o.State.(Uncertain)
	if !ok || in.Attempt != st.Attempt || in.Stage != st.Stage {
		return o, nil
	}
	switch in.Outcome {
	case ReconcileExactSuccess:
		switch r := st.Resume.(type) {
		case ResumeInit:
			if s, ok := st.Stage.(InitStage); ok {
				return advanceInit(o, r.Spec, st.Attempt, s, r.Done, in.Receipt, "newrepo.reconciled")
			}
		case ResumePublish:
			if s, ok := st.Stage.(PublishStage); ok {
				return advancePublish(o, r.Spec, st.Attempt, s, r.Done, in.Receipt, "newrepo.reconciled")
			}
		default:
		}
		return o, nil
	case ReconcileAbsent:
		// The effect is not there: the stage did not run. A retry needs a fresh confirmation.
		return stop(o, st.Stage, StopStageFailed, st.Resume, false)
	case ReconcileConflict:
		return stop(o, st.Stage, StopConflict, st.Resume, false)
	default: // ReconcileUnknown and anything else: stay Uncertain, write nothing
		return o, nil
	}
}

// --- retry ---

// retryable: a rejected or untracked operation never resumes; everything else
// needs a fresh human confirmation with the original preconditions.
func retryable(r StopReason) bool { return r != StopRejected && r != StopUntracked }

func stepRetry(o Operation, in RetryConfirmed) (Operation, []Command) {
	var (
		stage      Stage
		resume     Resumable
		minAttempt = 1
	)
	switch st := o.State.(type) {
	case Stopped:
		if !retryable(st.Reason) {
			return o, nil
		}
		stage, resume = st.Stage, st.Resume
	case Uncertain:
		stage, resume, minAttempt = st.Stage, st.Resume, st.Attempt+1
	default:
		return o, nil
	}
	if in.Attempt < minAttempt {
		return o, nil
	}
	switch r := resume.(type) {
	case ResumeInit:
		s, ok := stage.(InitStage)
		if !ok || initIndex(s) != len(r.Done) || in.Hash != r.Spec.Hash() {
			return o, nil
		}
		if !in.PreconditionsHold {
			return refuse(o, StopPreconditions)
		}
		return beginInit(o, r.Spec, in.Attempt, s, r.Done, "newrepo.retry")
	case ResumePublish:
		s, ok := stage.(PublishStage)
		if !ok || publishIndex(s) != len(r.Done) || in.Hash != r.Spec.Hash() {
			return o, nil
		}
		if !in.PreconditionsHold {
			return refuse(o, StopPreconditions)
		}
		return beginPublish(o, r.Spec, in.Attempt, s, r.Done, "newrepo.retry")
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
	switch o.State.(type) {
	case Reserved, AwaitingConfirm:
		return with(o, Stopped{Reason: StopRejected, Resume: ResumeNone{}}), []Command{
			PersistFailure{Operation: o.ID, Reason: string(StopRejected)},
			record(o, "newrepo.reject", nil, string(StopRejected)),
		}
	default:
		return o, nil
	}
}

func stepUntracked(o Operation) (Operation, []Command) {
	var (
		stage  Stage
		resume Resumable = ResumeNone{}
	)
	switch st := o.State.(type) {
	case Reserved, AwaitingConfirm, Ready:
	case Initializing:
		stage, resume = st.Stage, ResumeInit{Spec: st.Spec, Done: withReceipt(st.Done)}
	case Publishing:
		stage, resume = st.Stage, ResumePublish{Spec: st.Spec, Done: withReceipt(st.Done)}
	case Uncertain:
		stage, resume = st.Stage, st.Resume
	default: // nil, Published, Stopped
		return o, nil
	}
	return with(o, Stopped{Stage: stage, Reason: StopUntracked, Resume: resume}), []Command{
		PersistFailure{Operation: o.ID, Stage: stage, Reason: string(StopUntracked)},
		record(o, "newrepo.untracked", stage, string(StopUntracked)),
		CancelWorker{Operation: o.ID},
	}
}
