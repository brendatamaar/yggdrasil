package proposals

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// MaxPendingPerTask caps the pending proposals of one task.
const MaxPendingPerTask = 50

// Source says who created a proposal (sealed): an agent through MCP, or the
// user.
type Source interface{ isSource() }

// AgentSource: an agent proposed it.
type AgentSource struct{ Task core.TaskID }

// UserSource: the user created it.
type UserSource struct{}

func (AgentSource) isSource() {}
func (UserSource) isSource()  {}

// Proposal is a header plus a sealed State. A nil State means the proposal
// does not exist yet (only Created applies).
type Proposal struct {
	ID        core.ProposalID
	ChangeSet core.ChangeSetID
	Task      core.TaskID
	Repo      core.RepoID
	Source    Source
	Action    Action
	// ActionHash is Hash(Action): what the user confirms.
	ActionHash string
	// Summary is agent text, shown as plain text next to Describe(Action).
	Summary string
	Created time.Time
	State   State
}
