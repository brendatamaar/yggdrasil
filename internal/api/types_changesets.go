package api

import "github.com/brendatamaar/yggdrasil/internal/core"

// DTOs of change sets (RV-03, RV-04; docs/design/changesets.md). The handlers
// are in changesets.go.

// TopicChangeSets carries ChangeSetChangedMessage: a change set changed state,
// order or run, or the review notes of its task changed.
const TopicChangeSets = "changesets"

// ChangeSetChangedMessage is the data of a message on TopicChangeSets. Kind is
// "changeset" or "notes". It holds no text of a note (docs/AGENTS.md 5.9).
type ChangeSetChangedMessage struct {
	ChangeSetID core.ChangeSetID `json:"changeSetId,omitempty"`
	TaskID      core.TaskID      `json:"taskId"`
	Kind        string           `json:"kind"`
}

// The values of ChangeSetDTO.State.
const (
	ChangeSetStateOpen       = "open"
	ChangeSetStateConfirming = "confirming"
	ChangeSetStateShipped    = "shipped"
	ChangeSetStateClosed     = "closed"
)

// The values of ChangeSetDTO.Run.State.
const (
	RunStateIdle      = "idle"
	RunStateHalted    = "halted"
	RunStateCompleted = "completed"
)

// The only value of WarningDTO.Code at v0.5.
const WarningDependentBeforeDependency = "dependent_before_dependency"

// ScopeDTO says which proposals a run covers: Kind "all", or "repo" with RepoID.
type ScopeDTO struct {
	Kind   string      `json:"kind"`
	RepoID core.RepoID `json:"repoId,omitempty"`
}

// ChangeSetMemberDTO is one proposal of the set. Hash is what a confirm run
// must send back in Seen.
type ChangeSetMemberDTO struct {
	ProposalID core.ProposalID     `json:"proposalId"`
	RepoID     core.RepoID         `json:"repoId"`
	Kind       core.ProposalKind   `json:"kind"`
	Status     core.ProposalStatus `json:"status"`
	Hash       string              `json:"hash"`
}

// ChangeSetRepoDTO is a repo of the set, for display.
type ChangeSetRepoDTO struct {
	RepoID core.RepoID `json:"repoId"`
	Name   string      `json:"name"`
}

// MRLinkDTO is a merge request created by an executed member. URL is only set
// for a URL that passed the safe-URL check.
type MRLinkDTO struct {
	RepoID core.RepoID `json:"repoId"`
	URL    string      `json:"url"`
}

// WarningDTO is an advisory finding of the preflight. It never blocks; the
// user acknowledges it by Code. Message is plain words.
type WarningDTO struct {
	Code       string      `json:"code"`
	Dependent  core.RepoID `json:"dependent,omitempty"`
	Dependency core.RepoID `json:"dependency,omitempty"`
	Message    string      `json:"message"`
}

// RunDTO is how the last confirm run ended. At and Reason are set when it
// halted.
type RunDTO struct {
	State  string          `json:"state"`
	At     core.ProposalID `json:"at,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

// ConfirmingDTO is set while a run is in progress.
type ConfirmingDTO struct {
	Scope     ScopeDTO        `json:"scope"`
	Current   core.ProposalID `json:"current"`
	Remaining int             `json:"remaining"`
}

// ChangeSetDTO is a change set with its members, merge order, links and
// warnings. SuggestedOrder follows the repos' dependencies; Cycle lists repos
// in a dependency cycle (the suggestion then keeps the task's order). Landing
// kinds (merge_local, remove_worktree) appear as members but are never part of
// a run.
type ChangeSetDTO struct {
	ID             core.ChangeSetID     `json:"id"`
	TaskID         core.TaskID          `json:"taskId"`
	Status         core.ChangeSetStatus `json:"status"`
	State          string               `json:"state"`
	Repos          []ChangeSetRepoDTO   `json:"repos"`
	Order          []core.RepoID        `json:"order"`
	SuggestedOrder []core.RepoID        `json:"suggestedOrder"`
	Cycle          []core.RepoID        `json:"cycle,omitempty"`
	Members        []ChangeSetMemberDTO `json:"members"`
	Links          []MRLinkDTO          `json:"links"`
	Run            RunDTO               `json:"run"`
	Confirming     *ConfirmingDTO       `json:"confirming,omitempty"`
	Warnings       []WarningDTO         `json:"warnings"`
}

// SetOrderRequest is the body of PUT /api/changesets/{id}/order: a permutation
// of the set's repos.
type SetOrderRequest struct {
	Order []core.RepoID `json:"order"`
}

// PreflightRequest is the body of POST /api/changesets/{id}/preflight.
type PreflightRequest struct {
	Scope ScopeDTO `json:"scope"`
}

// PreflightResponse lists the warnings a run of Scope would raise.
type PreflightResponse struct {
	Warnings []WarningDTO `json:"warnings"`
}

// ConfirmRunRequest is the body of POST /api/changesets/{id}/confirm. Seen maps
// each proposal ID of the run to the hash the user saw; Ack lists the warning
// codes the user acknowledged.
type ConfirmRunRequest struct {
	Scope ScopeDTO          `json:"scope"`
	Seen  map[string]string `json:"seen"`
	Ack   []string          `json:"ack,omitempty"`
}
