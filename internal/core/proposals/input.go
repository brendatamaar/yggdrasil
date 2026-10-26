package proposals

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Input is one event for Step (sealed). Time arrives in Now.
type Input interface{ isInput() }

// Facts are what the shell checked before a proposal may be created.
type Facts struct {
	RepoTracked bool
	// PathsOK: every path is inside the task's worktree and, for kinds other
	// than AgentFile, not protected.
	PathsOK bool
	// PendingForTask is the number of pending proposals the task has.
	PendingForTask int
	// RemoteSupported: false for a merge request on a non-GitLab remote.
	RemoteSupported bool
	// TaskChangeSet is the task's existing change set ID, empty when it has
	// none yet. A non-empty value must equal the header's ChangeSet, so a
	// proposal never attaches to another change set.
	TaskChangeSet core.ChangeSetID
}

// Created: a new proposal (header and action) with the facts about it.
type Created struct {
	Proposal Proposal
	Facts    Facts
	Now      time.Time
}

// Confirm: the user confirmed the exact action hash they saw.
type Confirm struct {
	Hash        string
	By          string
	RepoTracked bool
	Now         time.Time
}

// Reject: the user rejected it.
type Reject struct {
	By  string
	Now time.Time
}

// ExecDone: the executor finished.
type ExecDone struct {
	Attempt int
	Result  Result
	Now     time.Time
}

// ExecFailed: the executor failed.
type ExecFailed struct {
	Attempt   int
	Kind      FailKind
	Message   string
	Retryable bool
	Now       time.Time
}

// Retry: a user click on a retryable failure; never automatic.
type Retry struct {
	By          string
	RepoTracked bool
	Now         time.Time
}

// Recovered: sent once at startup for each Confirmed proposal.
type Recovered struct{ Now time.Time }

// ReconcileOutcome is what a read-only check found (sealed set as constants).
type ReconcileOutcome string

const (
	ReconcileDone    ReconcileOutcome = "done"
	ReconcileNotDone ReconcileOutcome = "not_done"
	ReconcileUnknown ReconcileOutcome = "unknown"
)

// Reconciled: the answer to a Reconcile command.
type Reconciled struct {
	Attempt int
	Outcome ReconcileOutcome
	// Result is set when Outcome is ReconcileDone.
	Result Result
	Now    time.Time
}

func (Created) isInput()    {}
func (Confirm) isInput()    {}
func (Reject) isInput()     {}
func (ExecDone) isInput()   {}
func (ExecFailed) isInput() {}
func (Retry) isInput()      {}
func (Recovered) isInput()  {}
func (Reconciled) isInput() {}
