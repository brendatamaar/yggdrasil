package agents

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
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
	// Policy is what the guard enforces for this run: mode, readable and
	// writable roots and the command allowlist (SF-06). Adapters that enforce
	// through a hook write it where the hook reads it. The zero value is not
	// a valid policy; adapters that use it call Policy.Validate first.
	Policy guard.Policy
	// ConfigTrusted says the user has trusted the agent config the task's repos
	// ship (SF-09). Advisory: adapters that cannot make repo config inert
	// refuse without it; the Claude Code adapter ignores repo config entirely
	// and does not read this. guard.Admit keeps the SF-09 policy.
	ConfigTrusted bool
	// MCPConfigPath is the file WriteMCPConfig wrote; when set, the adapter
	// loads that MCP server and no other.
	MCPConfigPath string
}

// RepoConfigProvider is optionally implemented by an adapter whose CLI reads
// agent config from the repo (hooks, MCP servers, sub-agent definitions).
// The trust scan hashes these files (SF-09). Paths are relative to the repo
// root, with forward slashes; a folder means everything below it.
type RepoConfigProvider interface {
	ConfigPaths() []string
}

// ConfigNameProvider is optionally implemented next to RepoConfigProvider by an
// adapter whose CLI also loads config files by name at any depth (for example
// nested CLAUDE.md files). Names are bare file names, not paths.
type ConfigNameProvider interface {
	ConfigNames() []string
}

// MCPConfigurer is optionally implemented by an adapter that can be pointed at
// Yggdrasil's MCP server. WriteMCPConfig writes the CLI's MCP config file into
// dir and returns its path. The URL and the bearer token go into the file,
// never onto a command line (invariant 4); the adapter passes the file to the
// CLI, and only that server may load.
type MCPConfigurer interface {
	WriteMCPConfig(dir, url, token string) (path string, err error)
}

// Deny is one tool call the adapter's enforcement refused. It carries the
// tool's name and a guard reason message, never a path or any text of the call.
type Deny struct {
	Tool   string
	Reason string
	At     time.Time
}

// DenyReporter is optionally implemented by a Run whose adapter enforces
// through a hook that keeps a log. Denies lists the refusals of the run; call
// it after Wait. The tasks service audits each one as guard.deny.
type DenyReporter interface {
	Denies() []Deny
}

// ToolUseReporter is optionally implemented by a Run. SeenToolUses lists the
// IDs of the tool calls the agent's stream showed, in order; call it after
// Wait. The tasks service compares it with the enforcement log: a tool call
// without a log record means enforcement did not see it and must fail the task.
type ToolUseReporter interface {
	SeenToolUses() []string
}
