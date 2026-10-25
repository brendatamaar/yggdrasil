package proposals

import (
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// State is the lifecycle state of a proposal (sealed): Pending, Confirmed,
// Rejected, Executed or Failed. Status maps it onto the stored
// core.ProposalStatus.
type State interface {
	Status() core.ProposalStatus
	isState()
}

// Pending waits for the user.
type Pending struct{}

// Confirmed is executing (or interrupted by a crash, until Reconciled).
type Confirmed struct {
	Attempt int
	At      time.Time
	By      string
}

// Rejected is terminal.
type Rejected struct{ At time.Time }

// Executed is terminal. Result holds plain facts (commit ID, MR URL, moved
// paths, build ID).
type Executed struct {
	At     time.Time
	Result Result
}

// Failed leaves only through Retry, and only when Retryable.
type Failed struct {
	At        time.Time
	Kind      FailKind
	Message   string
	Retryable bool
}

func (Pending) isState()   {}
func (Confirmed) isState() {}
func (Rejected) isState()  {}
func (Executed) isState()  {}
func (Failed) isState()    {}

func (Pending) Status() core.ProposalStatus   { return core.ProposalPending }
func (Confirmed) Status() core.ProposalStatus { return core.ProposalConfirmed }
func (Rejected) Status() core.ProposalStatus  { return core.ProposalRejected }
func (Executed) Status() core.ProposalStatus  { return core.ProposalExecuted }
func (Failed) Status() core.ProposalStatus    { return core.ProposalFailed }

// FailKind says how an execution failed.
type FailKind string

const (
	FailStale         FailKind = "stale"
	FailPrecondition  FailKind = "precondition"
	FailRepoUntracked FailKind = "repo_untracked"
	FailRemote        FailKind = "rejected_by_remote"
	FailUnsafeRemote  FailKind = "unsafe_remote"
	FailInterrupted   FailKind = "interrupted"
	FailExec          FailKind = "exec_error"
)

// Validate reports whether k is known.
func (k FailKind) Validate() error {
	switch k {
	case FailStale, FailPrecondition, FailRepoUntracked, FailRemote, FailUnsafeRemote, FailInterrupted, FailExec:
		return nil
	}
	return fmt.Errorf("unknown failure kind %q", string(k))
}

// Result is what an executed proposal produced. Fields not meant for the
// kind stay empty.
type Result struct {
	CommitID   string
	MRURL      string
	MovedPaths []RelPath
	BuildID    core.BuildID
	// Note is a plain sentence, such as "pushed; open the MR from the repo
	// page".
	Note string
}
