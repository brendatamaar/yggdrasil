package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of proposals (RV-02, docs/design/proposals.md). The UI shows Lines,
// never only Summary: Lines is the exact words of what will run.

// TopicProposals carries ProposalChangedMessage: a proposal was created or
// changed state. The client refetches by ID.
const TopicProposals = "proposals"

// ProposalChangedMessage is the data of a message on TopicProposals.
type ProposalChangedMessage struct {
	ProposalID core.ProposalID     `json:"proposalId"`
	TaskID     core.TaskID         `json:"taskId"`
	RepoID     core.RepoID         `json:"repoId,omitempty"`
	Status     core.ProposalStatus `json:"status"`
}

// ProposalResultDTO is what an executed proposal produced.
type ProposalResultDTO struct {
	CommitID   string       `json:"commitId,omitempty"`
	MRURL      string       `json:"mrUrl,omitempty"`
	MovedPaths []string     `json:"movedPaths,omitempty"`
	BuildID    core.BuildID `json:"buildId,omitempty"`
	Note       string       `json:"note,omitempty"`
}

// ProposalDTO is one proposal. Source is "agent" or "user". FailKind, Message
// and Retryable are set on a failed proposal. ActionHash is what Confirm must
// send back.
type ProposalDTO struct {
	ID          core.ProposalID     `json:"id"`
	ChangeSetID core.ChangeSetID    `json:"changeSetId,omitempty"`
	TaskID      core.TaskID         `json:"taskId,omitempty"`
	RepoID      core.RepoID         `json:"repoId,omitempty"`
	RepoName    string              `json:"repoName,omitempty"`
	Kind        core.ProposalKind   `json:"kind"`
	Status      core.ProposalStatus `json:"status"`
	Source      string              `json:"source"`
	// FromAgent is true when an agent made the proposal.
	FromAgent bool `json:"fromAgent"`
	// Summary is the agent's own text, shown as plain text.
	Summary string `json:"summary"`
	// Lines is what the confirmation dialog shows.
	Lines      []string           `json:"lines"`
	ActionHash string             `json:"actionHash"`
	Attempt    int                `json:"attempt"`
	FailKind   string             `json:"failKind,omitempty"`
	Message    string             `json:"message,omitempty"`
	Retryable  bool               `json:"retryable,omitempty"`
	Result     *ProposalResultDTO `json:"result,omitempty"`
	CreatedAt  time.Time          `json:"createdAt"`
	DecidedAt  *time.Time         `json:"decidedAt,omitempty"`
}

// ProposalsResponse is the body of GET /api/proposals.
type ProposalsResponse struct {
	Proposals []ProposalDTO `json:"proposals"`
}

// ConfirmProposalRequest is the body of POST /api/proposals/{id}/confirm. Hash
// is the ActionHash the user saw (64 hex digits).
type ConfirmProposalRequest struct {
	Hash string `json:"hash"`
}
