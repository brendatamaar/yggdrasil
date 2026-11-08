package verify

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Input is one event for Step (sealed). Every input carries Now; build IDs
// arrive as fields because the core never generates them.
type Input interface{ isInput() }

// TurnEnded: the reconcile loop found the task idle in review with a turn
// newer than the aggregate's.
type TurnEnded struct {
	Turn TurnID
	// Seq is the turn's sequence number in its task (1 or more). A turn is
	// newer than the aggregate's when Seq is greater than its TurnSeq.
	Seq         int
	Facts       Facts
	Plan        Plan
	BootBuild   core.BuildID
	VerifyBuild core.BuildID
	Now         time.Time
}

// Retry: the user pressed "Verify again".
type Retry struct {
	Facts       Facts
	Plan        Plan
	BootBuild   core.BuildID
	VerifyBuild core.BuildID
	Now         time.Time
}

// Resume: startup reconcile for an Interrupted aggregate.
type Resume struct {
	Facts       Facts
	Plan        Plan
	BootBuild   core.BuildID
	VerifyBuild core.BuildID
	Now         time.Time
}

// Cancel: the user cancelled, or (Cause CancelNotTrusted) the repo stopped
// being trusted while its chain ran. An empty Cause is CancelUser.
type Cancel struct {
	Now   time.Time
	Cause CancelCause
}

// BuildFinished: the builds service reports a terminal build (at least once).
type BuildFinished struct {
	Build  core.BuildID
	Result BuildResult
	Now    time.Time
}

// EnqueueFailed: the interpreter's EnqueueBuild was rejected or gave up.
type EnqueueFailed struct {
	Build  core.BuildID
	Reason string
	Now    time.Time
}

func (TurnEnded) isInput()     {}
func (Retry) isInput()         {}
func (Resume) isInput()        {}
func (Cancel) isInput()        {}
func (BuildFinished) isInput() {}
func (EnqueueFailed) isInput() {}

// BuildResult is how a build ended (sealed). The shell maps builds.State to
// it (ToBuildResult).
type BuildResult interface{ isBuildResult() }

// BuildPassed: every step succeeded.
type BuildPassed struct{}

// BuildFailedAt: a step exited non-zero.
type BuildFailedAt struct{ Step, ExitCode int }

// BuildStartFailedAt: a step could not start.
type BuildStartFailedAt struct {
	Step   int
	Reason StartFailure
}

// BuildCancelled: the build was cancelled.
type BuildCancelled struct{}

// BuildInterrupted: ygg stopped while it ran.
type BuildInterrupted struct{}

func (BuildPassed) isBuildResult()        {}
func (BuildFailedAt) isBuildResult()      {}
func (BuildStartFailedAt) isBuildResult() {}
func (BuildCancelled) isBuildResult()     {}
func (BuildInterrupted) isBuildResult()   {}
