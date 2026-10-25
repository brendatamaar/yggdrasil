package guard

import "github.com/brendatamaar/yggdrasil/internal/core"

// Command is plain data that asks the shell for an effect (sealed). The core
// never performs one. No command writes to a working tree (invariant I4);
// there is no revert.
type Command interface{ isCommand() }

// TakeSnapshot asks for a read-only snapshot. The shell answers with
// SnapshotTaken or SnapshotFailed. Idempotent.
type TakeSnapshot struct {
	Turn  TurnID
	Phase Phase
}

// StopReason says why StopTask was emitted.
type StopReason string

const (
	StopCannotVerify StopReason = "cannot_verify"
	StopTreeChanged  StopReason = "tree_changed"
)

// StopTask asks the tasks service to stop the turn; it answers the tasks core
// with IntegrityViolated. Idempotent with key (Turn, Reason).
type StopTask struct {
	Turn   TurnID
	Reason StopReason
}

// FlagTask marks the task so the user sees what changed. Idempotent per turn.
type FlagTask struct {
	Turn    TurnID
	Changes []Change
}

// PersistTrust records that the user trusted Hash for Repo. Idempotent.
type PersistTrust struct {
	Repo core.RepoID
	Hash string
}

// ClearTrust forgets the trust of Repo. Idempotent.
type ClearTrust struct{ Repo core.RepoID }

// AuditEntry is the audit entry a command asks for. It holds repo-relative
// paths at most, never agent text or secrets.
type AuditEntry struct {
	// Action is "guard.integrity", "guard.trust", ...
	Action string
	// Outcome is a short code: "clean", "violated", "trusted", "trust_lost".
	Outcome string
	Turn    TurnID
	Repo    core.RepoID
	// Files are repo-relative paths of changed or trusted files.
	Files []string
}

// Audit asks the shell to write an entry in the transaction of the state it
// belongs to.
type Audit struct{ Entry AuditEntry }

func (TakeSnapshot) isCommand() {}
func (StopTask) isCommand()     {}
func (FlagTask) isCommand()     {}
func (PersistTrust) isCommand() {}
func (ClearTrust) isCommand()   {}
func (Audit) isCommand()        {}
