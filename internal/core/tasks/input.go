package tasks

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Input is one event for Step (sealed). Clock reads and generated IDs arrive
// as fields; the core reads neither.
type Input interface{ isInput() }

// Admission is guard.Admit's answer as the tasks core sees it. The shell
// calls guard.Admit and copies the result here (the two core packages do not
// import each other). Reason and Message are the guard's stable code and its
// plain-language text.
type Admission struct {
	Admitted bool
	// Warning is "", "edits_main_checkout" or "edits_all_repos".
	Warning string
	Reason  string
	Message string
}

// Message is a new user message: a new turn.
type Message struct {
	Turn      TurnID
	Mode      core.TaskMode
	Prompt    string
	Now       time.Time
	Admission Admission
	Caps      Caps
	// EditsMain lists the repos whose main checkout the turn would edit
	// (current_branch mode); the scheduler uses it.
	EditsMain []core.RepoID
}

// Admitted: Schedule picked the waiting turn; it moves to Starting.
type Admitted struct {
	Turn TurnID
	Now  time.Time
}

// Prepared: worktrees exist and locks are held.
type Prepared struct {
	Turn      TurnID
	Attempt   Attempt
	Dir       string
	ExtraDirs []string
	Now       time.Time
}

// PrepareFailed: worktrees or locks could not be prepared.
type PrepareFailed struct {
	Turn    TurnID
	Attempt Attempt
	Reason  string
	Now     time.Time
}

// LocksDenied: a main-checkout lock is held by Holder.
type LocksDenied struct {
	Turn    TurnID
	Attempt Attempt
	Repo    core.RepoID
	Holder  string
}

// RunStatus is how a run ended.
type RunStatus string

const (
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// RunFinished: the agent run ended.
type RunFinished struct {
	Turn      TurnID
	Attempt   Attempt
	Status    RunStatus
	StartFail bool // Failed before the agent produced anything (CLI missing, not signed in)
	Usage     Usage
	SessionID string
	Now       time.Time
}

// UsageReported: tokens and cost from the run, as an increment or as totals.
type UsageReported struct {
	Turn       TurnID
	Attempt    Attempt
	Usage      Usage
	Cumulative bool
}

// SubagentStarted: the run observed a sub-agent start.
type SubagentStarted struct {
	Turn    TurnID
	Attempt Attempt
	AgentID string
	Depth   int
}

// SubagentStopped: the run observed a sub-agent end.
type SubagentStopped struct {
	Turn    TurnID
	Attempt Attempt
	AgentID string
}

// CancelRequested: the user cancelled.
type CancelRequested struct{ Now time.Time }

// IntegrityViolated: guard.StepIntegrity asked to stop the turn.
type IntegrityViolated struct {
	Turn TurnID
	Now  time.Time
}

// ProcessesGone: KillTask finished.
type ProcessesGone struct {
	Turn    TurnID
	Attempt Attempt
}

// Dismissed is also an Outcome, so the input is named DismissRequested.

// DismissRequested: the user marked the task done.
type DismissRequested struct{ Now time.Time }

// Recovered: sent once at startup for a task found live in the database.
type Recovered struct{ Now time.Time }

func (Message) isInput()           {}
func (Admitted) isInput()          {}
func (Prepared) isInput()          {}
func (PrepareFailed) isInput()     {}
func (LocksDenied) isInput()       {}
func (RunFinished) isInput()       {}
func (UsageReported) isInput()     {}
func (SubagentStarted) isInput()   {}
func (SubagentStopped) isInput()   {}
func (CancelRequested) isInput()   {}
func (IntegrityViolated) isInput() {}
func (ProcessesGone) isInput()     {}
func (DismissRequested) isInput()  {}
func (Recovered) isInput()         {}
