package tasks

import "github.com/brendatamaar/yggdrasil/internal/core"

// Command is plain data that asks the shell for an effect (sealed). Step and
// Schedule return them; the core never performs one.
type Command interface{ isCommand() }

// PersistTurn stores the new turn. Idempotent with key Turn.
type PersistTurn struct {
	Turn TurnID
	Mode core.TaskMode
	// Prompt is stored, never audited or notified.
	Prompt string
}

// Enqueue puts the waiting turn on the board. Idempotent.
type Enqueue struct {
	Turn      TurnID
	Mode      core.TaskMode
	EditsMain []core.RepoID
}

// Requeue puts a turn back, keeping its place. Idempotent.
type Requeue struct{ Turn TurnID }

// Dequeue removes a waiting turn from the board. Idempotent.
type Dequeue struct{ Turn TurnID }

// PrepareRun creates worktrees and takes locks. Idempotent: repos that
// already have a worktree for the task are skipped.
type PrepareRun struct {
	Turn    TurnID
	Attempt Attempt
}

// StartRun starts the agent. NOT idempotent: protected by Attempt (K6).
type StartRun struct {
	Turn      TurnID
	Attempt   Attempt
	Mode      core.TaskMode
	Resume    bool
	Caps      Caps
	Dir       string
	ExtraDirs []string
}

// KillRun cancels the run context; KillTask follows. Idempotent.
type KillRun struct {
	Turn    TurnID
	Attempt Attempt
}

// FinishTurn stores the turn's usage and session. Idempotent with key
// (Turn, Attempt).
type FinishTurn struct {
	Turn      TurnID
	Attempt   Attempt
	Usage     Usage
	SessionID string
}

// ReleaseLocks frees the main-checkout locks of the task. Idempotent.
type ReleaseLocks struct{}

// KillTask stops every process of the task. Idempotent.
type KillTask struct{}

// Publish tells the hub about the new status. Idempotent.
type Publish struct{ Status core.TaskStatus }

// AuditEntry is the audit entry a command asks for. It never holds the
// prompt, agent text or paths.
type AuditEntry struct {
	// Action is "task.turn", "task.start", "task.finish", "subagent.cap", ...
	Action  string
	Outcome string
	Turn    TurnID
}

// Audit asks the shell to write an entry in the transaction of the state it
// belongs to.
type Audit struct{ Entry AuditEntry }

// Reject tells the caller why a message was refused; no state changes.
type Reject struct {
	Reason  string
	Message string
}

// Admit is emitted by Schedule: the shell feeds Admitted to the task.
type Admit struct{ Task core.TaskID }

func (PersistTurn) isCommand()  {}
func (Enqueue) isCommand()      {}
func (Requeue) isCommand()      {}
func (Dequeue) isCommand()      {}
func (PrepareRun) isCommand()   {}
func (StartRun) isCommand()     {}
func (KillRun) isCommand()      {}
func (FinishTurn) isCommand()   {}
func (ReleaseLocks) isCommand() {}
func (KillTask) isCommand()     {}
func (Publish) isCommand()      {}
func (Audit) isCommand()        {}
func (Reject) isCommand()       {}
func (Admit) isCommand()        {}
