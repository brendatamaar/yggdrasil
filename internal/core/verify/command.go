package verify

import "github.com/brendatamaar/yggdrasil/internal/core"

// Command is plain data that asks the shell for an effect (sealed).
type Command interface{ isCommand() }

// EnqueueBuild starts a build in the task's worktree. Idempotent with key
// Build: the builds service returns the existing build for a known ID.
type EnqueueBuild struct {
	Build core.BuildID
	Stage Stage
	Spec  BuildSpec
}

// CancelBuild cancels a build. Idempotent; cancelling a finished or unknown
// build is a no-op.
type CancelBuild struct{ Build core.BuildID }

// WriteAudit records a state change. It runs in the same transaction as the
// state write.
type WriteAudit struct {
	Action  string
	Outcome string
	Turn    TurnID
}

// Publish tells subscribers that the verification changed. Idempotent.
type Publish struct{ Task core.TaskID }

func (EnqueueBuild) isCommand() {}
func (CancelBuild) isCommand()  {}
func (WriteAudit) isCommand()   {}
func (Publish) isCommand()      {}
