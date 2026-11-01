package changesets

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Input is one event for StepSet (sealed). Every input carries Now. The state
// itself is built by the shell from rows (StateFromRows), not by an input.
type Input interface{ isInput() }

// MembersChanged: a member's status, hash or kind changed, or a member was
// added.
type MembersChanged struct {
	Members []Member
	Links   map[core.RepoID]string
	Now     time.Time
}

// SetOrder: the user edited the merge order.
type SetOrder struct {
	Order []core.RepoID
	Deps  []Edge
	Now   time.Time
}

// ConfirmRun: the user pressed "Confirm all" or "Confirm repo".
type ConfirmRun struct {
	Scope Scope
	// Seen is the hash the user saw for each proposal.
	Seen map[core.ProposalID]string
	// Ack lists the warning codes the user acknowledged.
	Ack  []WarningCode
	Deps []Edge
	Now  time.Time
}

// StopRun: the user stopped the run.
type StopRun struct{ Now time.Time }

// ConfirmRefused: proposals.Confirm refused (stale hash, not pending,
// untracked repo).
type ConfirmRefused struct {
	ID     core.ProposalID
	Reason string
	Now    time.Time
}

// Close: the user closed the set.
type Close struct{ Now time.Time }

func (MembersChanged) isInput() {}
func (SetOrder) isInput()       {}
func (ConfirmRun) isInput()     {}
func (StopRun) isInput()        {}
func (ConfirmRefused) isInput() {}
func (Close) isInput()          {}
