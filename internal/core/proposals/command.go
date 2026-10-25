package proposals

import "github.com/brendatamaar/yggdrasil/internal/core"

// Command is plain data that asks the shell for an effect (sealed).
type Command interface{ isCommand() }

// Persist stores a new proposal. Idempotent with key ID.
type Persist struct{ Proposal Proposal }

// Execute runs the action. NOT idempotent, but fenced: each executor checks
// its precondition first. Emitted once per (ID, Attempt) (P2).
type Execute struct {
	ID      core.ProposalID
	Attempt int
	Action  Action
}

// Reconcile checks read-only whether a crashed execution landed. Idempotent.
type Reconcile struct {
	ID      core.ProposalID
	Attempt int
	Action  Action
}

// AuditEntry is the audit entry a command asks for. It holds no prompt, token
// or absolute path.
type AuditEntry struct {
	// Action is "proposal.create", "proposal.confirm", "proposal.execute",
	// "proposal.fail", "proposal.retry", "proposal.reject".
	Action   string
	Proposal core.ProposalID
	Kind     core.ProposalKind
	// Outcome is the fail kind for proposal.fail, else empty.
	Outcome string
}

// Audit asks the shell to write an entry in the transaction of the state it
// belongs to.
type Audit struct{ Entry AuditEntry }

// Publish tells the hub about the new status.
type Publish struct{ Status core.ProposalStatus }

// RefuseReason says why an input was refused.
type RefuseReason string

const (
	RefuseHashMismatch  RefuseReason = "hash_mismatch"
	RefuseRepoUntracked RefuseReason = "repo_untracked"
	RefuseTooMany       RefuseReason = "too_many_pending"
	RefuseBadFacts      RefuseReason = "bad_facts"
	RefuseUnsupported   RefuseReason = "unsupported"
)

// Refuse tells the caller (HTTP or MCP) why; no state changes.
type Refuse struct{ Reason RefuseReason }

func (Persist) isCommand()   {}
func (Execute) isCommand()   {}
func (Reconcile) isCommand() {}
func (Audit) isCommand()     {}
func (Publish) isCommand()   {}
func (Refuse) isCommand()    {}
