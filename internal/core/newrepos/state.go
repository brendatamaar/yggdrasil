package newrepos

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Receipt is what a finished stage left behind (sealed): exactly the identity
// the next stage and the reconciler need, and nothing else. A stage is never
// recorded as done by a flag; its receipt is the proof.
//
//sumtype:decl
type Receipt interface{ isReceipt() }

// DirReceipt: the folder was created. Identity is an opaque, platform-owned
// handle identity (volume/file ID) that proves a later resume sees the same
// folder and not a swapped or reparsed one.
type DirReceipt struct{ Identity string }

// GitReceipt: git init ran. GitDirIdentity is the identity of the .git folder.
type GitReceipt struct{ GitDirIdentity string }

// FileReceipt: one file was created exclusively with exactly this content.
type FileReceipt struct {
	Name        string
	ContentHash string
}

// CommitReceipt: the first commit exists with this exact tree, message hash
// and author, and no parent. Finding any HEAD is not proof.
type CommitReceipt struct {
	Commit      string
	Tree        string
	MessageHash string
	Author      string
}

// TrackedReceipt: tracking was finalized, the repo row is ready.
type TrackedReceipt struct{}

// RemoteReceipt: the remote repo exists. RemoteID is the host's immutable
// repository ID; URL is credential-free.
type RemoteReceipt struct {
	RemoteID string
	URL      string
}

// OriginReceipt: origin was added (it was absent) pointing at URL.
type OriginReceipt struct{ URL string }

// PushReceipt: the remote branch holds exactly Head.
type PushReceipt struct{ Head string }

func (DirReceipt) isReceipt()     {}
func (GitReceipt) isReceipt()     {}
func (FileReceipt) isReceipt()    {}
func (CommitReceipt) isReceipt()  {}
func (TrackedReceipt) isReceipt() {}
func (RemoteReceipt) isReceipt()  {}
func (OriginReceipt) isReceipt()  {}
func (PushReceipt) isReceipt()    {}

// State is where an operation is (sealed): Reserved, AwaitingConfirm,
// Initializing, Ready, Publishing, Published, Uncertain or Stopped.
//
//sumtype:decl
type State interface{ isState() }

// Reserved: the repo row and setup draft exist; no disk effect happened.
type Reserved struct {
	Spec InitSpec
	Hash string
}

// AwaitingConfirm: the initialization proposal waits for the user.
type AwaitingConfirm struct {
	Spec     InitSpec
	Proposal core.ProposalID
	Hash     string
}

// Initializing: Attempt runs Stage. Done holds the receipts of the stages
// finished so far, in order.
type Initializing struct {
	Spec    InitSpec
	Attempt int
	Stage   InitStage
	Done    []Receipt
}

// Ready: initialized, finalized and tracked; InitialHead is the first commit.
// Publishing is optional from here.
type Ready struct {
	Repo        core.RepoID
	InitialHead string
}

// Publishing: Attempt runs Stage of Spec. Done holds the publish receipts so far.
type Publishing struct {
	Spec    PublishSpec
	Attempt int
	Stage   PublishStage
	Done    []Receipt
}

// Published: the remote exists and holds the first commit.
type Published struct {
	RemoteID string
	URL      string
	Head     string
}

// Uncertain: the outcome of Stage's last attempt is unknown (lost reply,
// cancellation, timeout, restart). Only a read-only reconciliation moves it;
// the stage is never repeated automatically.
type Uncertain struct {
	Attempt int
	Stage   Stage
	Reason  string
	// Resume is the stage state to resume from after an exact-success
	// reconciliation or a confirmed retry.
	Resume Resumable
}

// Stopped: a definite failure or a rejection. Nothing is rolled back by
// deleting; a retry needs a fresh human confirmation with the original
// immutable preconditions still valid.
type Stopped struct {
	Stage  Stage
	Reason StopReason
	Resume Resumable
	// Attempt is the attempt that ran Stage (0 when none ran). A retry needs a
	// strictly greater attempt, so a late result of the old attempt can never
	// be taken for the retry's.
	Attempt int
}

func (Reserved) isState()        {}
func (AwaitingConfirm) isState() {}
func (Initializing) isState()    {}
func (Ready) isState()           {}
func (Publishing) isState()      {}
func (Published) isState()       {}
func (Uncertain) isState()       {}
func (Stopped) isState()         {}

// Resumable is what a retry or reconciliation continues from (sealed):
// ResumeInit or ResumePublish. Keeping the specs here makes a retry
// impossible without the original immutable preconditions.
//
//sumtype:decl
type Resumable interface{ isResumable() }

// ResumeInit continues initialization with the receipts so far.
type ResumeInit struct {
	Spec InitSpec
	Done []Receipt
}

// ResumePublish continues publishing with the receipts so far.
type ResumePublish struct {
	Spec PublishSpec
	Done []Receipt
}

// ResumeNone: nothing to resume (rejected before a stage ran).
type ResumeNone struct{}

func (ResumeInit) isResumable()    {}
func (ResumePublish) isResumable() {}
func (ResumeNone) isResumable()    {}

// StopReason says why an operation stopped. It is a closed set.
type StopReason string

const (
	StopRejected       StopReason = "rejected"
	StopUntracked      StopReason = "untracked"
	StopPathExists     StopReason = "path_exists"
	StopMissingAuthor  StopReason = "missing_author"
	StopStageFailed    StopReason = "stage_failed"
	StopHostRefused    StopReason = "host_refused"
	StopNameCollision  StopReason = "name_collision"
	StopOriginExists   StopReason = "origin_exists"
	StopConflict       StopReason = "conflict"
	StopUnsupported    StopReason = "unsupported_transport"
	StopPreconditions  StopReason = "preconditions_changed"
	StopNotImplemented StopReason = "not_implemented"
)

// Validate reports whether r is a known reason.
func (r StopReason) Validate() error {
	switch r {
	case StopRejected, StopUntracked, StopPathExists, StopMissingAuthor, StopStageFailed, StopHostRefused,
		StopNameCollision, StopOriginExists, StopConflict, StopUnsupported, StopPreconditions, StopNotImplemented:
		return nil
	}
	return invalid("unknown stop reason %q", string(r))
}

// Operation is the header of one new-repo operation plus its sealed State. A
// nil State means it does not exist yet (only Reserve applies). Version is the
// optimistic version of the stored row.
type Operation struct {
	ID      core.OperationID
	Project core.ProjectID
	Repo    core.RepoID
	Version int64
	State   State
}

// IDs are the identifiers the shell generated for a reservation.
type IDs struct {
	Operation core.OperationID
	Repo      core.RepoID
	SetupTask core.TaskID
	Proposal  core.ProposalID
}

// Input is one event for Step (sealed).
//
//sumtype:decl
type Input interface{ isInput() }

// Reserve: the user asked to create a new repo. ProjectTracked and LeafFree are
// facts the shell checked (the leaf is free in the project's reservations and
// the target path does not exist); Author says a git author is configured.
type Reserve struct {
	Spec           InitSpec
	IDs            IDs
	ProjectTracked bool
	LeafFree       bool
	HasAuthor      bool
	Now            time.Time
}

// ProposalCreated: the initialization proposal exists with this hash.
type ProposalCreated struct {
	ID   core.ProposalID
	Hash string
}

// Confirmed: the user confirmed the exact hash. The shell supplies a fresh
// attempt number.
type Confirmed struct {
	Hash    string
	Attempt int
	Now     time.Time
}

// StageSucceeded: Attempt finished Stage with Receipt.
type StageSucceeded struct {
	Attempt int
	Stage   Stage
	Receipt Receipt
	Now     time.Time
}

// StageFailed: Attempt failed Stage with a classified outcome.
type StageFailed struct {
	Attempt int
	Stage   Stage
	Outcome FailOutcome
	// Reason classifies a definite failure. Empty (or unknown) means
	// StopStageFailed. It is ignored for OutcomeUnknown.
	Reason StopReason
	Now    time.Time
}

// PublishConfirmed: the user confirmed the exact publish hash for a ready repo.
// OriginAbsent and HeadMatches are facts the shell just read.
type PublishConfirmed struct {
	Spec         PublishSpec
	Hash         string
	Attempt      int
	OriginAbsent bool
	HeadMatches  bool
	Now          time.Time
}

// Recovered: sent once at startup for every operation in Initializing or
// Publishing.
type Recovered struct{ Now time.Time }

// Reconciled: the answer to ReconcileStage.
type Reconciled struct {
	Attempt int
	Stage   Stage
	Outcome ReconcileOutcome
	Receipt Receipt
	Now     time.Time
}

// RetryConfirmed: a fresh human confirmation to retry a Stopped or Uncertain
// stage, with the original immutable hash and the facts rechecked.
type RetryConfirmed struct {
	Hash              string
	Attempt           int
	PreconditionsHold bool
	Now               time.Time
}

// Reject: the user rejected the pending proposal.
type Reject struct{ Now time.Time }

// Untracked: the project or repo was untracked.
type Untracked struct{ Now time.Time }

func (Reserve) isInput()          {}
func (ProposalCreated) isInput()  {}
func (Confirmed) isInput()        {}
func (StageSucceeded) isInput()   {}
func (StageFailed) isInput()      {}
func (PublishConfirmed) isInput() {}
func (Recovered) isInput()        {}
func (Reconciled) isInput()       {}
func (RetryConfirmed) isInput()   {}
func (Reject) isInput()           {}
func (Untracked) isInput()        {}

// FailOutcome says how a stage ended when it did not succeed. Unknown is its
// own case: cancellation, timeout and a lost reply are Unknown, never failure.
type FailOutcome string

const (
	// OutcomeDefiniteFailure: the stage did not take effect (precondition,
	// refused, permission, collision). The reason is a StopReason.
	OutcomeDefiniteFailure FailOutcome = "definite_failure"
	// OutcomeUnknown: the stage may or may not have taken effect.
	OutcomeUnknown FailOutcome = "unknown"
)

// ReconcileOutcome is what a read-only check found.
type ReconcileOutcome string

const (
	ReconcileExactSuccess ReconcileOutcome = "exact_success"
	ReconcileAbsent       ReconcileOutcome = "absent"
	ReconcileConflict     ReconcileOutcome = "conflict"
	ReconcileUnknown      ReconcileOutcome = "unknown"
)

// Command is plain data asking the shell for an effect (sealed).
//
//sumtype:decl
type Command interface{ isCommand() }

// PersistReservationAndSetupDraft stores, in one transaction, the reserved repo
// row, the internal setup draft task, its task-repo link and the audit entry.
// No disk effect.
type PersistReservationAndSetupDraft struct {
	Operation core.OperationID
	Spec      InitSpec
	IDs       IDs
}

// PublishPreview shows the confirmation of a spec's hash.
type PublishPreview struct {
	Hash string
}

// PersistIntent stores the intent of a stage attempt before the effect runs.
type PersistIntent struct {
	Operation core.OperationID
	Attempt   int
	Stage     Stage
}

// RunInitStage performs one local stage. Not idempotent; exclusive-create and
// content/HEAD fences make a repeat safe to detect.
type RunInitStage struct {
	Operation core.OperationID
	Attempt   int
	Stage     InitStage
	Spec      InitSpec
	Done      []Receipt
}

// RunPublishStage performs one publish stage. CreateRemote is one non-idempotent
// POST; AddOrigin a local write allowed only while origin is absent; PushHead an
// in-process, non-forced smart-HTTP push.
type RunPublishStage struct {
	Operation core.OperationID
	Attempt   int
	Stage     PublishStage
	Spec      PublishSpec
	Done      []Receipt
}

// PersistReceipt stores the receipt of a succeeded stage with the new state.
type PersistReceipt struct {
	Operation core.OperationID
	Attempt   int
	Stage     Stage
	Receipt   Receipt
}

// PersistFailure stores a stopped or uncertain state with its reason.
type PersistFailure struct {
	Operation core.OperationID
	Stage     Stage
	Reason    string
}

// Audit asks for the audit entry of a state change in the same transaction.
type Audit struct {
	Action    string
	Operation core.OperationID
	Stage     string
	Outcome   string
}

// ReconcileStage asks one bounded read-only question about a stage's effect.
type ReconcileStage struct {
	Operation core.OperationID
	Attempt   int
	Stage     Stage
}

// CancelWorker stops the worker of an operation.
type CancelWorker struct{ Operation core.OperationID }

// Explain tells the caller in plain words why nothing happened.
type Explain struct{ Reason StopReason }

func (PersistReservationAndSetupDraft) isCommand() {}
func (PublishPreview) isCommand()                  {}
func (PersistIntent) isCommand()                   {}
func (RunInitStage) isCommand()                    {}
func (RunPublishStage) isCommand()                 {}
func (PersistReceipt) isCommand()                  {}
func (PersistFailure) isCommand()                  {}
func (Audit) isCommand()                           {}
func (ReconcileStage) isCommand()                  {}
func (CancelWorker) isCommand()                    {}
func (Explain) isCommand()                         {}
