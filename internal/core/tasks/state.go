package tasks

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// State is the lifecycle state of one task (sealed): Draft, Waiting,
// Starting, Running, Stopping or Idle. Status maps it onto the stored
// core.TaskStatus.
type State interface {
	Status() core.TaskStatus
	isState()
}

// Draft: the task exists and has no turn yet.
type Draft struct{}

// Waiting: a turn is queued.
type Waiting struct {
	Turn TurnID
	// Attempt is the number of attempts already made for Turn (0 for a turn
	// that never started). A new admission becomes Attempt+1, so attempts
	// never decrease and an input from an earlier attempt stays stale (K4, K5).
	Attempt   Attempt
	BlockedBy Blocker
}

// Starting: worktrees and locks are being prepared.
type Starting struct {
	Turn    TurnID
	Attempt Attempt
	Since   time.Time
}

// Running: the agent runs.
type Running struct {
	Turn      TurnID
	Attempt   Attempt
	Since     time.Time
	Subagents []Subagent
	Usage     Usage
}

// Stopping: a kill was requested; the task still has a process.
type Stopping struct {
	Turn    TurnID
	Attempt Attempt
	Since   time.Time
	Cause   StopCause
	// Usage is carried over from Running so FinishTurn does not lose it.
	Usage Usage
}

// Idle: no run is live. It accepts a new message whatever the outcome.
type Idle struct {
	Outcome Outcome
	At      time.Time
}

func (Draft) isState()    {}
func (Waiting) isState()  {}
func (Starting) isState() {}
func (Running) isState()  {}
func (Stopping) isState() {}
func (Idle) isState()     {}

func (Draft) Status() core.TaskStatus    { return core.TaskDraft }
func (Waiting) Status() core.TaskStatus  { return core.TaskQueued }
func (Starting) Status() core.TaskStatus { return core.TaskRunning }
func (Running) Status() core.TaskStatus  { return core.TaskRunning }
func (Stopping) Status() core.TaskStatus { return core.TaskRunning }

// Status of an Idle task follows its outcome. A nil Outcome is not a valid
// state; it reports needs_you so the user looks at it.
func (i Idle) Status() core.TaskStatus {
	if i.Outcome == nil {
		return core.TaskNeedsYou
	}
	return i.Outcome.status()
}

// Blocker says why a waiting task has not started (sealed).
type Blocker interface{ isBlocker() }

// BlockedNone: not blocked by anything yet, just queued.
type BlockedNone struct{}

// BlockedMaxAgents: maxAgents tasks are already active.
type BlockedMaxAgents struct{}

// BlockedMainCheckout: another task, build or git action holds the main
// checkout of Repo.
type BlockedMainCheckout struct {
	Repo   core.RepoID
	Holder string
}

func (BlockedNone) isBlocker()         {}
func (BlockedMaxAgents) isBlocker()    {}
func (BlockedMainCheckout) isBlocker() {}

// StopCause says why a task is stopping (sealed set as constants).
type StopCause string

const (
	CauseUserCancel        StopCause = "user_cancel"
	CauseIntegrityViolated StopCause = "integrity_violated"
	CauseCapExceeded       StopCause = "cap_exceeded"
)

// NeedsWhy says why an idle task needs the user.
type NeedsWhy string

const (
	NeedsIntegrityViolated NeedsWhy = "integrity_violated"
	NeedsCapExceeded       NeedsWhy = "cap_exceeded"
)

// FailKind says how a failed turn failed.
type FailKind string

const (
	FailPrepare     FailKind = "prepare_failed"
	FailStart       FailKind = "start_failed"
	FailRun         FailKind = "run_failed"
	FailInterrupted FailKind = "interrupted"
)

// Outcome is how a task went idle (sealed): Answered, ReadyForReview,
// NeedsYou, Failed, Cancelled or Dismissed.
type Outcome interface {
	status() core.TaskStatus
	isOutcome()
}

// Answered: an Ask turn finished.
type Answered struct{}

// ReadyForReview: a writing turn finished.
type ReadyForReview struct{}

// NeedsYou: the turn stopped because something needs a decision.
type NeedsYou struct{ Why NeedsWhy }

// Failed: the turn did not complete.
type Failed struct{ Kind FailKind }

// Cancelled: the user cancelled.
type Cancelled struct{}

// Dismissed: the user marked the task done.
type Dismissed struct{}

func (Answered) status() core.TaskStatus       { return core.TaskDone }
func (ReadyForReview) status() core.TaskStatus { return core.TaskReview }
func (NeedsYou) status() core.TaskStatus       { return core.TaskNeedsYou }
func (Failed) status() core.TaskStatus         { return core.TaskFailed }
func (Cancelled) status() core.TaskStatus      { return core.TaskCancelled }
func (Dismissed) status() core.TaskStatus      { return core.TaskDone }

func (Answered) isOutcome()       {}
func (ReadyForReview) isOutcome() {}
func (NeedsYou) isOutcome()       {}
func (Failed) isOutcome()         {}
func (Cancelled) isOutcome()      {}
func (Dismissed) isOutcome()      {}
