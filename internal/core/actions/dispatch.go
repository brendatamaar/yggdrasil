package actions

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Submission is the state of one submission: the request, its digest, the
// highest attempt issued and a sealed State. A nil State is Idle's zero value
// and is treated as Idle.
type Submission struct {
	Request Request
	// Digest is Request.Digest(), computed once by the shell at Requested.
	Digest  string
	Seq     int
	Version int64
	State   State
}

// State is where a submission is (sealed): Idle, Preparing, Ready,
// Submitting, Completed, Refused or Cancelled.
//
//sumtype:decl
type State interface{ isState() }

// Idle: nothing started.
type Idle struct{}

// Preparing: facts and context are being loaded for Attempt.
type Preparing struct{ Attempt int }

// Ready: the preview is shown. ExpectedHead pins the repo HEAD (and MR head)
// the preview was made against; a changed scope refuses the submit.
type Ready struct {
	Context      PreparedContext
	ExpectedHead string
}

// Submitting: intent is persisted and dispatch runs. Key is the idempotency
// key the effects carry: "action:<requestID>:<attempt>".
type Submitting struct {
	Attempt int
	Key     string
}

// Completed: the effect exists. For an import the TaskID is a DRAFT task;
// moving a draft to the queue is an explicit, separate task submission.
type Completed struct{ Result Result }

// Refused: nothing was or will be started for this request.
type Refused struct{ Reason RefuseReason }

// Cancelled: the user cancelled before submission. After submission the task
// is cancelled through the task endpoint, not here.
type Cancelled struct{}

func (Idle) isState()       {}
func (Preparing) isState()  {}
func (Ready) isState()      {}
func (Submitting) isState() {}
func (Completed) isState()  {}
func (Refused) isState()    {}
func (Cancelled) isState()  {}

// PreparedContext is the bounded, redacted, source-labelled text attached to
// the task prompt, plus what could not be read.
type PreparedContext struct {
	// Source labels where the text came from ("CI run 123 on shop-api").
	Source string
	// Text is at most MaxContextBytes.
	Text      string
	Truncated bool
	// Unavailable lists labelled gaps (no token, expired log, unreadable folder).
	Unavailable      []string
	Availability     ContextAvailability
	WaitingForFolder bool
}

// Result is what a completed submission refers to. TaskID is set for task and
// draft kinds, ProposalIDs for proposal kinds, BatchID for a group of commit
// proposals. Fields not meant for the kind stay empty.
type Result struct {
	TaskID      core.TaskID
	ProposalIDs []core.ProposalID
	BatchID     string
}

// RefuseReason says why a submission was refused. It is a closed set.
type RefuseReason string

const (
	RefuseInvalid         RefuseReason = "invalid_request"
	RefuseUntracked       RefuseReason = "untracked"
	RefuseNoOffering      RefuseReason = "no_offering"
	RefuseScopeChanged    RefuseReason = "scope_changed"
	RefuseCapability      RefuseReason = "capability_missing"
	RefuseStaleHead       RefuseReason = "stale_head"
	RefuseDigestMismatch  RefuseReason = "digest_mismatch"
	RefuseAdmission       RefuseReason = "admission_refused"
	RefusePrepareFailed   RefuseReason = "prepare_failed"
	RefuseAbsentAfterLoss RefuseReason = "absent_after_lost_reply"
)

// Validate reports whether r is a known reason.
func (r RefuseReason) Validate() error {
	switch r {
	case RefuseInvalid, RefuseUntracked, RefuseNoOffering, RefuseScopeChanged, RefuseCapability,
		RefuseStaleHead, RefuseDigestMismatch, RefuseAdmission, RefusePrepareFailed, RefuseAbsentAfterLoss:
		return nil
	}
	return errUnknown("action refusal", string(r))
}

// SubmitOutcome is how a dispatch attempt failed (sealed set as constants).
type SubmitOutcome string

const (
	// SubmitDefiniteRefusal: admission or validation said no; nothing exists.
	SubmitDefiniteRefusal SubmitOutcome = "definite_refusal"
	// SubmitUnknown: timeout or crash after intent; the effect may exist.
	SubmitUnknown SubmitOutcome = "unknown"
)

// ReconcileOutcome is what a read-only lookup of the durable task/proposal
// link found.
type ReconcileOutcome string

const (
	ReconcileFound    ReconcileOutcome = "found"
	ReconcileAbsent   ReconcileOutcome = "definitely_absent"
	ReconcileNoAnswer ReconcileOutcome = "unknown"
)

// IDs are the identifiers the shell generated for the effects of one request,
// so the core never makes one.
type IDs struct {
	Task      core.TaskID
	Proposals []core.ProposalID
	Batch     string
}

// Input is one event for Step (sealed).
//
//sumtype:decl
type Input interface{ isInput() }

// Requested: a valid, parsed submission with the IDs and time the shell chose.
type Requested struct {
	Request Request
	IDs     IDs
	Now     time.Time
}

// Prepared: Attempt loaded the facts and context.
type Prepared struct {
	Attempt int
	Facts   Facts
	Context PreparedContext
}

// PrepareFailed: Attempt could not load enough to offer anything.
type PrepareFailed struct {
	Attempt int
	Reason  RefuseReason
}

// SubmitRequested: the user pressed the submit button of the preview.
// ScopeHead is the repo HEAD now, checked against Ready.ExpectedHead.
type SubmitRequested struct {
	ScopeHead string
	Now       time.Time
}

// Submitted: the effect for Attempt exists.
type Submitted struct {
	Attempt int
	Result  Result
}

// SubmitFailed: the effect of Attempt failed with Outcome.
type SubmitFailed struct {
	Attempt int
	Outcome SubmitOutcome
	Reason  RefuseReason
}

// Cancel: the user closed the preview before submitting.
type Cancel struct{}

// Recovered: sent once at startup for every request left in Submitting.
type Recovered struct{ Now time.Time }

// Reconciled: the answer to ReconcileDispatch.
type Reconciled struct {
	Attempt int
	Outcome ReconcileOutcome
	Result  Result
}

func (Requested) isInput()       {}
func (Prepared) isInput()        {}
func (PrepareFailed) isInput()   {}
func (SubmitRequested) isInput() {}
func (Submitted) isInput()       {}
func (SubmitFailed) isInput()    {}
func (Cancel) isInput()          {}
func (Recovered) isInput()       {}
func (Reconciled) isInput()      {}

// Command is plain data asking the shell for an effect (sealed).
//
//sumtype:decl
type Command interface{ isCommand() }

// LoadFactsAndContext: one bounded aggregate read for Attempt. Idempotent.
type LoadFactsAndContext struct {
	Attempt int
	Request Request
}

// PresentPreview shows the preview. Idempotent.
type PresentPreview struct{ Context PreparedContext }

// Explain tells the caller why nothing was started. Idempotent.
type Explain struct{ Reason RefuseReason }

// PersistIntent stores the intent and the outbox row in one transaction with
// the RequestID, digest and attempt, before any effect.
type PersistIntent struct {
	Request Request
	Digest  string
	Attempt int
	Key     string
}

// Dispatch performs the effect: one draft task / task, or a bounded set of
// proposals, with the request's key in the same transaction as the links.
type Dispatch struct {
	Attempt int
	Key     string
	Request Request
	Context PreparedContext
	IDs     IDs
}

// PersistResult stores the result links of a request.
type PersistResult struct {
	Attempt int
	Result  Result
}

// Audit asks for the audit entry of the state change, in the same transaction.
type Audit struct {
	Action  string
	Request core.RequestID
	Kind    Kind
	Outcome string
}

// Publish tells the hub the request changed. Carries no prompt or context.
type Publish struct{ Request core.RequestID }

// ReconcileDispatch looks up the durable task/proposal link for the request.
// One read, idempotent. Never starts a second effect.
type ReconcileDispatch struct {
	Attempt int
	Key     string
}

// CancelPreparation stops the load of an attempt.
type CancelPreparation struct{ Attempt int }

func (LoadFactsAndContext) isCommand() {}
func (PresentPreview) isCommand()      {}
func (Explain) isCommand()             {}
func (PersistIntent) isCommand()       {}
func (Dispatch) isCommand()            {}
func (PersistResult) isCommand()       {}
func (Audit) isCommand()               {}
func (Publish) isCommand()             {}
func (ReconcileDispatch) isCommand()   {}
func (CancelPreparation) isCommand()   {}

// Step is (submission2, commands) = Step(submission1, input) following the table
// of docs/design/phase-6-actions.md. An input that does not apply returns the
// dispatch unchanged and no commands.
//
// PENDING (task A): the foundation freezes the types; the transitions are not
// implemented. Every input returns the submission unchanged and no commands, so
// the dispatcher can never report a start that did not happen. It must not be
// wired before the orchestration task lands.
func Step(d Submission, in Input) (Submission, []Command) {
	switch in.(type) {
	case Requested, Prepared, PrepareFailed, SubmitRequested, Submitted,
		SubmitFailed, Cancel, Recovered, Reconciled:
		return d, nil
	}
	return d, nil
}
