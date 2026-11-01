package changesets

import "github.com/brendatamaar/yggdrasil/internal/core"

// Command is plain data that asks the shell for an effect (sealed).
type Command interface{ isCommand() }

// ConfirmProposal asks the proposals service to confirm one proposal with the
// hash the user saw. Idempotent: proposals refuses a second confirm.
type ConfirmProposal struct {
	ID   core.ProposalID
	Hash string
}

// WriteAudit records a change-set change in the same transaction as the state
// write.
type WriteAudit struct {
	Action  string
	Outcome string
	Set     core.ChangeSetID
}

// Publish tells subscribers that the set changed. Idempotent.
type Publish struct{ Set core.ChangeSetID }

func (ConfirmProposal) isCommand() {}
func (WriteAudit) isCommand()      {}
func (Publish) isCommand()         {}
