package agents

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Adapter runs one agent CLI and converts its output into Events (AG-01).
// Interfaces live where they are used; this one is used by the runner.
//
// Rules for implementations (docs/AGENTS.md 5.5):
//   - start every process through platform.Start with the clean environment;
//   - fill Event.ParentID from the CLI's own sub-agent marker, apply the
//     sub-agent caps in StartOptions and block agent-native worktrees;
//   - never emit or log raw stream lines (they can carry account data);
//   - return plain-language errors from Check.
type Adapter interface {
	// Name is the stable adapter ID, e.g. "claude-code" or "mock".
	Name() string
	// Capabilities says what this adapter supports.
	Capabilities() Capabilities
	// Check verifies prerequisites (CLI installed, signed in) and returns a
	// plain-language error that says what to do next (AG-05).
	Check(ctx context.Context) error
	// Start begins a run. Cancelling ctx stops the run and every process it
	// started. The returned Run is always closed by the caller via Wait.
	Start(ctx context.Context, opts StartOptions) (Run, error)
}

// Run is one running agent session.
type Run interface {
	// Events delivers normalized events, in order. It is closed when the run
	// has ended and every event has been delivered.
	Events() <-chan Event
	// SessionID is the resumable session ID, once the agent has reported it.
	SessionID() string
	// Wait blocks until the run ends and returns why it failed, if it did.
	Wait() error
}

// StartOptions describe one run.
type StartOptions struct {
	TaskID core.TaskID
	Prompt string
	Mode   core.TaskMode
	Model  string
	// Dir is the working directory: the task's worktree, in long path form.
	Dir string
	// ExtraDirs are further directories the agent may touch (project mode).
	ExtraDirs []string
	// ResumeSessionID continues an earlier session when Capabilities.Resume.
	ResumeSessionID string
	// Env is the clean environment from internal/safety; never nil.
	Env []string
	// Limits for agent-native sub-agents (AG-22).
	MaxSubagentDepth       int
	MaxConcurrentSubagents int
	// BudgetUSD is the spend cap for the run; 0 means none (AG-16).
	BudgetUSD float64
}
