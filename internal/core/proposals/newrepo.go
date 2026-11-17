package proposals

import "github.com/brendatamaar/yggdrasil/internal/core"

// The InitRepo and PublishRepo proposals hand their work to a durable new-repo
// lifecycle (docs/design/phase-6-newrepos.md) that runs in stages and may take
// longer than one call. The proposal therefore stays Confirmed until the
// operation reaches an outcome; this file decides, purely, what the executor
// does with the operation it finds and how the proposal follows the outcome.
//
// The shell reads the operation, maps its state onto an OperationView, and acts
// on the plan. Nothing here repeats a stage: a plan that could start work is
// only offered when nothing has run, or when a human retry follows a definite
// stop. An unknown outcome (a stage that may have partly run) is only ever
// reconciled, read-only.

// OperationPhase is where a new-repo operation stands, reduced to what a
// proposal cares about. It is a closed set.
type OperationPhase string

const (
	// OpIdle: reserved, or awaiting the confirmation. Nothing ran.
	OpIdle OperationPhase = "idle"
	// OpReady: initialized and tracked; publishing has not started.
	OpReady OperationPhase = "ready"
	// OpRunning: a durable worker owns a stage.
	OpRunning OperationPhase = "running"
	// OpUncertain: a stage's outcome is unknown; only reconciliation moves it.
	OpUncertain OperationPhase = "uncertain"
	// OpStopped: a definite failure or rejection; a retry needs a new confirmation.
	OpStopped OperationPhase = "stopped"
	// OpPublished: the remote exists and holds the first commit.
	OpPublished OperationPhase = "published"
)

// OperationFlow says which of the two flows a running, uncertain or stopped
// operation is in. FlowNone: no stage ran.
type OperationFlow string

const (
	FlowNone    OperationFlow = ""
	FlowInit    OperationFlow = "init"
	FlowPublish OperationFlow = "publish"
)

// Stop reasons the proposal knows how to explain. They mirror the lifecycle's
// reasons by value; any other reason is treated as an ordinary failed stage.
const (
	stopRejected       = "rejected"
	stopUntracked      = "untracked"
	stopConflict       = "conflict"
	stopNotImplemented = "not_implemented"
	stopPathExists     = "path_exists"
	stopMissingAuthor  = "missing_author"
	stopHostRefused    = "host_refused"
	stopNameCollision  = "name_collision"
	stopOriginExists   = "origin_exists"
	stopUnsupported    = "unsupported_transport"
	stopPreconditions  = "preconditions_changed"
)

// OperationView is what the executor knows of an operation.
type OperationView struct {
	Phase OperationPhase
	Flow  OperationFlow
	// StopReason is the lifecycle's reason when Phase is OpStopped.
	StopReason string
	// Head is the first commit (OpReady) or the pushed head (OpPublished).
	Head string
	// URL is the credential-free remote address (OpPublished).
	URL string
}

// NewRepoPlan is what the executor does next.
type NewRepoPlan string

const (
	// PlanNewRepoStart: call the lifecycle's confirm, once, for this attempt.
	// Only offered when nothing ran yet.
	PlanNewRepoStart NewRepoPlan = "start"
	// PlanNewRepoRetry: the operation stopped definitely at a stage and this
	// confirmed attempt is the human's fresh confirmation to retry it. Call the
	// lifecycle's retry, once. A confirm cannot resume a stopped operation.
	PlanNewRepoRetry NewRepoPlan = "retry"
	// PlanNewRepoDone: the work is already finished; report it, call nothing.
	PlanNewRepoDone NewRepoPlan = "done"
	// PlanNewRepoWait: a worker owns the operation; call nothing and wait for it.
	PlanNewRepoWait NewRepoPlan = "wait"
	// PlanNewRepoReconcile: a stage may have partly run; ask the lifecycle for
	// its read-only reconciliation and follow the outcome. Never confirm.
	PlanNewRepoReconcile NewRepoPlan = "reconcile"
	// PlanNewRepoRefuse: the operation is not in a state this proposal can act on.
	PlanNewRepoRefuse NewRepoPlan = "refuse"
)

// StopRetryable reports whether a fresh confirmation can help after the
// lifecycle stopped for reason. A rejection, an untracked project, an
// impossible input and a missing implementation never resume.
func StopRetryable(reason string) bool {
	switch reason {
	case stopRejected, stopUntracked, stopConflict, stopNotImplemented:
		return false
	}
	return true
}

// PlanNewRepo decides what the executor of a kind (InitRepo or PublishRepo)
// does with the operation as it is now. It never plans a start or a retry while
// a worker owns the operation or its outcome is unknown.
func PlanNewRepo(kind core.ProposalKind, v OperationView) NewRepoPlan {
	switch kind {
	case core.ProposalInitRepo:
		return planInit(v)
	case core.ProposalPublish:
		return planPublish(v)
	}
	return PlanNewRepoRefuse
}

func planInit(v OperationView) NewRepoPlan {
	switch v.Phase {
	case OpIdle:
		return PlanNewRepoStart
	case OpReady, OpPublished:
		return PlanNewRepoDone
	case OpRunning:
		switch v.Flow {
		case FlowInit:
			return PlanNewRepoWait
		case FlowPublish:
			return PlanNewRepoDone // initialization finished before publishing began
		}
	case OpUncertain:
		switch v.Flow {
		case FlowInit:
			return PlanNewRepoReconcile
		case FlowPublish:
			return PlanNewRepoDone
		}
	case OpStopped:
		switch {
		case v.Flow == FlowPublish:
			return PlanNewRepoDone
		case v.Flow == FlowInit && StopRetryable(v.StopReason):
			return PlanNewRepoRetry
		}
	}
	return PlanNewRepoRefuse
}

func planPublish(v OperationView) NewRepoPlan {
	switch v.Phase {
	case OpReady:
		return PlanNewRepoStart
	case OpPublished:
		return PlanNewRepoDone
	case OpRunning:
		if v.Flow == FlowPublish {
			return PlanNewRepoWait
		}
	case OpUncertain:
		if v.Flow == FlowPublish {
			return PlanNewRepoReconcile
		}
	case OpStopped:
		if v.Flow == FlowPublish && StopRetryable(v.StopReason) {
			return PlanNewRepoRetry
		}
	}
	return PlanNewRepoRefuse
}

// VerdictKind is how a proposal follows its operation. It is a closed set.
type VerdictKind string

const (
	// VerdictNotStarted: nothing ran for this proposal; a human may retry.
	VerdictNotStarted VerdictKind = "not_started"
	// VerdictPending: a worker owns the operation; the proposal stays Confirmed.
	VerdictPending VerdictKind = "pending"
	// VerdictUnknown: the outcome is unknown; only reconciliation settles it.
	VerdictUnknown VerdictKind = "unknown"
	// VerdictDone: the operation finished what this proposal asked.
	VerdictDone VerdictKind = "done"
	// VerdictFailed: the operation stopped definitely.
	VerdictFailed VerdictKind = "failed"
)

// Verdict is the judgement of an operation for a proposal.
type Verdict struct {
	Kind      VerdictKind
	Result    Result   // VerdictDone
	FailKind  FailKind // VerdictFailed
	Message   string   // VerdictFailed: plain language
	Retryable bool     // VerdictFailed
}

// JudgeOperation maps the operation's state onto the outcome of a proposal of
// kind: Ready (init) or Published (publish) is done; a definite stop is a
// failure that keeps the reason; a running worker is pending; an uncertain
// stage is unknown and is never reported as done or failed.
func JudgeOperation(kind core.ProposalKind, v OperationView) Verdict {
	switch kind {
	case core.ProposalInitRepo:
		return judgeInit(v)
	case core.ProposalPublish:
		return judgePublish(v)
	}
	return Verdict{Kind: VerdictUnknown}
}

func judgeInit(v OperationView) Verdict {
	switch v.Phase {
	case OpReady, OpPublished:
		return Verdict{Kind: VerdictDone, Result: Result{CommitID: initHead(v), Note: "The new repo is initialized and tracked. The first commit is on branch main."}}
	case OpIdle:
		return Verdict{Kind: VerdictNotStarted}
	case OpRunning, OpUncertain, OpStopped:
		if v.Flow == FlowPublish {
			return Verdict{Kind: VerdictDone, Result: Result{Note: "The new repo is initialized and tracked."}}
		}
	}
	return judgeStage(v)
}

func initHead(v OperationView) string {
	if v.Phase == OpReady {
		return v.Head
	}
	return ""
}

func judgePublish(v OperationView) Verdict {
	switch v.Phase {
	case OpPublished:
		note := "Published. The remote repository has the first commit."
		if v.URL != "" {
			note = "Published to " + v.URL + ". The remote repository has the first commit."
		}
		return Verdict{Kind: VerdictDone, Result: Result{CommitID: v.Head, Note: note}}
	case OpIdle, OpReady:
		return Verdict{Kind: VerdictNotStarted}
	}
	if v.Flow != FlowPublish {
		return Verdict{Kind: VerdictFailed, FailKind: FailPrecondition, Message: "The repo is not ready to publish. Nothing was sent to the host."}
	}
	return judgeStage(v)
}

// judgeStage judges a running, uncertain or stopped operation of the flow the
// proposal asked for.
func judgeStage(v OperationView) Verdict {
	switch v.Phase {
	case OpRunning:
		return Verdict{Kind: VerdictPending}
	case OpUncertain:
		return Verdict{Kind: VerdictUnknown}
	case OpStopped:
		kind, msg := explainStop(v.StopReason)
		return Verdict{Kind: VerdictFailed, FailKind: kind, Message: msg, Retryable: StopRetryable(v.StopReason) && v.Flow != FlowNone}
	}
	return Verdict{Kind: VerdictUnknown}
}

// explainStop is the plain-language reason of a stopped operation. No path,
// host answer or token is part of it.
func explainStop(reason string) (FailKind, string) {
	const noDelete = " Nothing was deleted or rolled back."
	switch reason {
	case stopRejected:
		return FailPrecondition, "This was rejected, so nothing was created." + noDelete
	case stopUntracked:
		return FailRepoUntracked, "The project is no longer tracked, so the work stopped." + noDelete + " Track the project again and start a new repo."
	case stopPathExists:
		return FailPrecondition, "A folder with that name already exists, so nothing was created or overwritten. Choose another name."
	case stopMissingAuthor:
		return FailPrecondition, "Git has no author name and email set up, so the first commit could not be made. Set them, then retry."
	case stopHostRefused:
		return FailRemote, "The host refused the request. Check the account, its permissions and the host token, then retry." + noDelete
	case stopNameCollision:
		return FailRemote, "The host already has a repository with that name. Nothing was changed there. Choose another name and ask for a new publish."
	case stopOriginExists:
		return FailPrecondition, "This repo already has an origin, which is never replaced. Nothing was changed."
	case stopUnsupported:
		return FailPrecondition, "Yggdrasil cannot publish this repo on its own. The local repo is kept; publish it by hand."
	case stopPreconditions:
		return FailStale, "Something changed since you confirmed this, so the work stopped before it went on." + noDelete + " Ask for a new proposal."
	case stopConflict, stopNotImplemented:
		return FailExec, "Yggdrasil stopped because the state it found was not one it can continue from." + noDelete + " Look at the repo before trying again."
	}
	return FailExec, "A step did not finish and the work stopped." + noDelete + " Check the repo, then retry."
}

// ReconcileOf maps a verdict onto the outcome Step takes for a Reconcile: only
// done is proven; nothing running or unknown is claimed either way.
func ReconcileOf(v Verdict) (ReconcileOutcome, Result) {
	switch v.Kind {
	case VerdictDone:
		return ReconcileDone, v.Result
	case VerdictNotStarted, VerdictFailed:
		return ReconcileNotDone, Result{}
	}
	return ReconcileUnknown, Result{}
}

// FreshAttempt is the attempt number to give the lifecycle for the confirmation
// of proposal attempt n: strictly above every attempt the operation has seen,
// so a late result of an earlier attempt can never be taken for this one.
func FreshAttempt(n int, seen []int) int {
	out := max(n, 1)
	for _, s := range seen {
		out = max(out, s+1)
	}
	return out
}
