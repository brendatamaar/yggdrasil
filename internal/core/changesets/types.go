package changesets

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// LinkMarker is the one line in an MR description template that the MR
// executor replaces with the rendered link block (RenderLinks).
const LinkMarker = "<!-- ygg:links -->"

// Limits.
const (
	MaxRepos   = 64
	MaxMembers = 512
)

// Header identifies a change set and the repos of its task.
type Header struct {
	ID    core.ChangeSetID
	Task  core.TaskID
	Repos []core.RepoID
	// Order is the merge order: a permutation of Repos.
	Order []core.RepoID
}

// Validate checks the IDs and that Order is a permutation of Repos.
func (h Header) Validate() error {
	if err := errors.Join(h.ID.Validate(), h.Task.Validate()); err != nil {
		return err
	}
	if len(h.Repos) == 0 || len(h.Repos) > MaxRepos {
		return fmt.Errorf("a change set needs 1 to %d repos", MaxRepos)
	}
	for _, r := range h.Repos {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	if !IsPermutation(h.Order, h.Repos) {
		return errors.New("the merge order must list each repo of the set exactly once")
	}
	return nil
}

// IsPermutation reports whether order lists each of repos exactly once.
func IsPermutation(order, repos []core.RepoID) bool {
	if len(order) != len(repos) {
		return false
	}
	a := slices.Clone(order)
	b := slices.Clone(repos)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b) && len(slices.Compact(a)) == len(a)
}

// Member is one proposal of the set, loaded from the proposals rows.
type Member struct {
	ID     core.ProposalID
	Repo   core.RepoID
	Kind   core.ProposalKind
	Status core.ProposalStatus
	// Hash is the proposal's action hash (64 hex).
	Hash string
}

// Validate checks every field.
func (m Member) Validate() error {
	if len(m.Hash) != 64 || strings.Trim(m.Hash, "0123456789abcdef") != "" {
		return errors.New("member hash must be 64 lowercase hex characters")
	}
	return errors.Join(m.ID.Validate(), m.Repo.Validate(), m.Kind.Validate(), m.Status.Validate())
}

// Scope says which members a run covers (sealed): ScopeAll or ScopeRepo.
type Scope interface {
	Validate() error
	isScope()
}

// ScopeAll: every pending member.
type ScopeAll struct{}

// ScopeRepo: the pending members of one repo.
type ScopeRepo struct{ Repo core.RepoID }

func (ScopeAll) isScope()  {}
func (ScopeRepo) isScope() {}

// Validate always succeeds.
func (ScopeAll) Validate() error { return nil }

// Validate checks the repo ID.
func (s ScopeRepo) Validate() error { return s.Repo.Validate() }

// Planned is a member a run will confirm, with the hash the user saw.
type Planned struct {
	ID   core.ProposalID
	Hash string
}

// RunResult is how the last run ended (sealed).
type RunResult interface{ isRunResult() }

// RunIdle: no run has happened.
type RunIdle struct{}

// RunHalted: a refused, failed or rejected member (or the user) stopped the
// run; the rest stay pending.
type RunHalted struct {
	At     core.ProposalID
	Reason string
}

// RunCompleted: every planned member was confirmed and reached a terminal
// status without a problem.
type RunCompleted struct{}

func (RunIdle) isRunResult()      {}
func (RunHalted) isRunResult()    {}
func (RunCompleted) isRunResult() {}

// Edge says Dependent depends on Dependency (read by the shell from the
// toolchains' Orderer).
type Edge struct{ Dependent, Dependency core.RepoID }

// WarningCode is a machine-readable warning the user acknowledges by code.
type WarningCode string

// WarningDependentBeforeDependency: in the plan, a repo is ordered before a
// repo it depends on.
const WarningDependentBeforeDependency WarningCode = "dependent_before_dependency"

// Validate reports whether c is known.
func (c WarningCode) Validate() error {
	if c == WarningDependentBeforeDependency {
		return nil
	}
	return fmt.Errorf("unknown warning code %q", string(c))
}

// Warning is one advisory finding of Preflight. It never blocks.
type Warning struct {
	Code       WarningCode
	Dependent  core.RepoID
	Dependency core.RepoID
}
