package changesets

import (
	"errors"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// State is the state of one change set (sealed): Open, Confirming, Shipped
// or Closed. Status maps it onto the stored core.ChangeSetStatus.
type State interface {
	Status() core.ChangeSetStatus
	Validate() error
	isState()
}

// Open: members are loaded from the proposals rows, never copied.
type Open struct {
	Set     Header
	Members []Member
	// Links is the MR URL by repo, from executed merge_request members.
	Links   map[core.RepoID]string
	LastRun RunResult
}

// Confirming: a confirm-all or per-repo run is in progress.
type Confirming struct {
	Open
	Scope Scope
	// Queue is the proposals still to confirm, with the hash the user saw.
	Queue []Planned
	// Current is confirmed and waiting to reach a terminal status.
	Current core.ProposalID
}

// Shipped: every member terminal, at least one executed, none failed.
type Shipped struct{ Open }

// Closed: every member rejected, or closed by the user with none pending.
type Closed struct{ Open }

func (Open) isState()       {}
func (Confirming) isState() {}
func (Shipped) isState()    {}
func (Closed) isState()     {}

// Status is open.
func (Open) Status() core.ChangeSetStatus { return core.ChangeSetOpen }

// Status is open.
func (Confirming) Status() core.ChangeSetStatus { return core.ChangeSetOpen }

// Status is shipped.
func (Shipped) Status() core.ChangeSetStatus { return core.ChangeSetShipped }

// Status is closed.
func (Closed) Status() core.ChangeSetStatus { return core.ChangeSetClosed }

// Validate checks the header, the members (each in a repo of the set) and the
// last run.
func (s Open) Validate() error {
	if err := s.Set.Validate(); err != nil {
		return err
	}
	if len(s.Members) > MaxMembers {
		return errors.New("a change set has too many proposals")
	}
	for _, m := range s.Members {
		if err := m.Validate(); err != nil {
			return err
		}
		found := false
		for _, r := range s.Set.Repos {
			found = found || r == m.Repo
		}
		if !found {
			return errors.New("a proposal belongs to a repo outside the change set")
		}
	}
	if s.LastRun == nil {
		return errors.New("a change set needs a last run result (RunIdle at least)")
	}
	return nil
}

// Validate checks the open part, the scope and the current proposal.
func (s Confirming) Validate() error {
	if s.Scope == nil {
		return errors.New("a confirming change set needs a scope")
	}
	return errors.Join(s.Open.Validate(), s.Scope.Validate(), s.Current.Validate())
}

// Validate checks the open part.
func (s Shipped) Validate() error { return s.Open.Validate() }

// Validate checks the open part.
func (s Closed) Validate() error { return s.Open.Validate() }
