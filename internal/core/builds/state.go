package builds

import (
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// State is the state of one build. It is a closed set: None, Queued, Running,
// Cancelling and Finished.
type State interface{ isState() }

// None is the state before Enqueue.
type None struct{}

// Queued waits for admission.
type Queued struct {
	ID       core.BuildID
	Spec     Spec
	QueuedAt time.Time
	Actor    Actor
}

// Running has one step running. Steps has one entry per spec step; before
// Current every step is Done(Succeeded), after it every step is Pending.
type Running struct {
	ID        core.BuildID
	Spec      Spec
	QueuedAt  time.Time
	StartedAt time.Time
	Actor     Actor
	Steps     []StepState
	Current   int
}

// Cancelling has sent the kill and waits for the process to exit. The data is
// that of Running; Steps[Current] is still StepRunning.
type Cancelling struct {
	Running
	CancelledAt time.Time
}

// Finished is terminal. No step is Pending or Running.
type Finished struct {
	ID       core.BuildID
	Spec     Spec
	QueuedAt time.Time
	Actor    Actor
	// StartedAt is nil when the build was cancelled while queued.
	StartedAt *time.Time
	// CancelledAt is set exactly when Result is ResultCancelled (I11).
	CancelledAt *time.Time
	FinishedAt  time.Time
	Steps       []StepState
	Result      Result
	// Artifacts is nil until collected; an empty slice means none were found.
	Artifacts []Artifact
}

func (None) isState()       {}
func (Queued) isState()     {}
func (Running) isState()    {}
func (Cancelling) isState() {}
func (Finished) isState()   {}

// StepState is the state of one step. It is a closed set: StepPending,
// StepRunning, StepDone and StepSkipped.
type StepState interface{ isStepState() }

// StepPending has not started.
type StepPending struct{}

// StepRunning is the one live step.
type StepRunning struct {
	Attempt   int
	StartedAt time.Time
}

// StepDone has ended with an Outcome.
type StepDone struct {
	Attempt      int
	StartedAt    time.Time
	FinishedAt   time.Time
	Outcome      Outcome
	LogTruncated bool
}

// StepSkipped never ran: an earlier step failed or the build was cancelled.
type StepSkipped struct{}

func (StepPending) isStepState() {}
func (StepRunning) isStepState() {}
func (StepDone) isStepState()    {}
func (StepSkipped) isStepState() {}

// Outcome is how a step ended. It is a closed set.
type Outcome interface{ isOutcome() }

// Succeeded: exit code 0.
type Succeeded struct{}

// Failed: a non-zero exit code.
type Failed struct{ ExitCode int }

// StartFailed: the process could not start.
type StartFailed struct{ Reason StartFailure }

// Cancelled: the user cancelled while the step ran.
type Cancelled struct{}

// Interrupted: ygg stopped while the step ran.
type Interrupted struct{}

func (Succeeded) isOutcome()   {}
func (Failed) isOutcome()      {}
func (StartFailed) isOutcome() {}
func (Cancelled) isOutcome()   {}
func (Interrupted) isOutcome() {}

// Result is how a finished build ended. It is a closed set.
type Result interface{ isResult() }

// ResultSucceeded: every step succeeded.
type ResultSucceeded struct{}

// ResultFailed: step Step failed or did not start.
type ResultFailed struct{ Step int }

// ResultCancelled: the user cancelled.
type ResultCancelled struct{}

// ResultInterrupted: ygg stopped while step Step ran.
type ResultInterrupted struct{ Step int }

func (ResultSucceeded) isResult()   {}
func (ResultFailed) isResult()      {}
func (ResultCancelled) isResult()   {}
func (ResultInterrupted) isResult() {}

// StartFailure says why a step process could not start.
type StartFailure string

const (
	StartNotFound   StartFailure = "not_found"
	StartBadArgs    StartFailure = "bad_args"
	StartDirMissing StartFailure = "dir_missing"
	StartOther      StartFailure = "other"
)

// Validate reports whether f is a known start failure.
func (f StartFailure) Validate() error {
	switch f {
	case StartNotFound, StartBadArgs, StartDirMissing, StartOther:
		return nil
	}
	return fmt.Errorf("unknown start failure %q", string(f))
}

// Status derives the stored build status from the state (the value of
// builds.status). None has no status and returns "".
func Status(s State) core.BuildStatus {
	switch st := s.(type) {
	case Queued:
		return core.BuildQueued
	case Running, Cancelling:
		return core.BuildRunning
	case Finished:
		switch st.Result.(type) {
		case ResultSucceeded:
			return core.BuildSucceeded
		case ResultFailed:
			return core.BuildFailed
		case ResultCancelled:
			return core.BuildCancelled
		case ResultInterrupted:
			return core.BuildInterrupted
		}
	}
	return ""
}
