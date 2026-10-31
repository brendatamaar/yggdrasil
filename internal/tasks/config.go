// Package tasks is the shell around internal/core/tasks: it parses messages and
// rows into core types, loads a task, calls tasks.Step and tasks.Schedule,
// persists the new state and its audit entries in one Store.Commit with the
// expected version, and then interprets the commands (worktrees, locks, the
// agent run, kills, publishing). Decisions live in the core; this package only
// does the effects (docs/AGENTS.md 5.11, docs/design/tasks.md).
package tasks

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
)

// Registry finds an agent adapter by the name a task stores.
type Registry interface {
	Get(name string) (agents.Adapter, bool)
}

// Worktrees creates the worktrees of a task (internal/worktrees.Ensurer).
// The service calls it for one task at a time.
type Worktrees interface {
	Ensure(ctx context.Context, task core.TaskID, repos []worktrees.RepoRef) ([]core.Worktree, error)
}

// MCPSessions gives a run its MCP session (MCP-06). BeginRun issues a token and
// writes the agent's MCP config into dir, returning its path ("" when the agent
// cannot use the tools). EndRun revokes the task's session of that attempt
// only (a late end of an older attempt changes nothing; attempt 0 means any
// attempt, for recovery); it is safe to call
// when there is none, and twice.
type MCPSessions interface {
	BeginRun(a agents.Adapter, task core.TaskID, attempt int, dir string) (configPath string, err error)
	EndRun(task core.TaskID, attempt int)
}

// Release gives a main-checkout lock back. It is safe to call twice.
type Release func()

// MainLock is the shared registry of main-checkout editors (AG-11): builds,
// git actions and tasks that edit a main checkout exclude each other.
// TryAcquire never blocks. On refusal it returns who holds the lock.
type MainLock interface {
	TryAcquire(repos []core.RepoID, owner string) (rel Release, holder string, ok bool)
}

// Hub tells connected clients. Implementations must not block.
type Hub interface {
	// Status announces a task's new status (title and status only: nothing
	// else may leave through a notification, docs/AGENTS.md 5.9).
	Status(m StatusMessage)
	// Event announces a stored transcript event.
	Event(task core.TaskID, seq int64, e agents.Event)
}

// StatusMessage is what Hub.Status carries.
type StatusMessage struct {
	TaskID core.TaskID
	Title  string
	Status core.TaskStatus
}

// Machine reports the room on this computer, for the maxAgents hint (D7).
type Machine interface {
	CPUs() int
	FreeMemMB() int
}

// Config wires a Service. Store, Adapters, Worktrees, MainLock, Scanner,
// Snapshotter, Env, Redactor, Hub, Machine, Killer, Now and NewID are
// required.
type Config struct {
	Store       *db.Store
	Adapters    Registry
	Worktrees   Worktrees
	MainLock    MainLock
	Scanner     safety.ConfigScanner
	Snapshotter safety.Snapshotter
	Env         safety.EnvBuilder
	Redactor    safety.Redactor
	Hub         Hub
	Machine     Machine
	// Killer stops every process of a task (platform.KillTask).
	Killer func(taskID string) error
	Now    func() time.Time
	// NewID returns an ID with the prefix, e.g. "tsk_9f86d081884c7d65". It
	// must be safe for concurrent use.
	NewID func(prefix string) string

	// Allowlist returns the command rules of a repo (seeded from the
	// toolchain defaults). Nil means no command may run (SF-05).
	Allowlist func(ctx context.Context, repo core.Repo) ([]guard.CommandRule, error)
	// PendingProposals counts the pending proposals of a task for the board.
	// Nil means none.
	PendingProposals func(ctx context.Context, task core.TaskID) (int, error)

	// MCP issues and revokes the per-run MCP sessions. Nil means no run gets
	// Yggdrasil's tools. RunsDir (an absolute folder outside every worktree) is
	// where each run's config file goes; it is required when MCP is set.
	MCP     MCPSessions
	RunsDir string

	Log *slog.Logger
	// Caps are the sub-agent limits of every turn (AG-22). Default 2 and 4.
	Caps coretasks.Caps
	// DefaultMax is maxAgents when the setting is missing. Default 2.
	DefaultMax int
	// UsageEvery is the least time between two batches of usage and
	// sub-agent inputs. Default 250 ms.
	UsageEvery time.Duration
	// SnapshotTimeout bounds one integrity snapshot; a snapshot that does not
	// answer in time is a lost reply (TurnAborted). Default 2 minutes.
	SnapshotTimeout time.Duration
	// SnapshotRetry is the wait before the one retry of a failed snapshot.
	// Default 1 second.
	SnapshotRetry time.Duration
	// FeedRetry is the base wait between the tries to store a result of the
	// service itself. Default 20 ms.
	FeedRetry time.Duration
	// WriteModes allows the modes that edit files (code, current_branch,
	// project). Off means Ask only.
	WriteModes bool
	// LockRetry is how often waiting tasks get another chance when a
	// main-checkout lock was denied. Default 3 seconds.
	LockRetry time.Duration
}

func (c *Config) validate() error {
	switch {
	case c.Store == nil, c.Adapters == nil, c.Worktrees == nil, c.MainLock == nil, c.Scanner == nil,
		c.Snapshotter == nil, c.Env == nil, c.Redactor == nil, c.Hub == nil, c.Machine == nil,
		c.Killer == nil, c.Now == nil, c.NewID == nil:
		return errors.New("tasks: store, adapters, worktrees, main lock, scanner, snapshotter, environment, redactor, hub, machine, killer, clock and id source are required")
	}
	if c.MCP != nil && c.RunsDir == "" {
		return errors.New("tasks: a folder for the runs' MCP config files is required with MCP sessions")
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Caps == (coretasks.Caps{}) {
		c.Caps = coretasks.Caps{Depth: 2, Concurrent: 4}
	}
	if c.DefaultMax == 0 {
		c.DefaultMax = coretasks.DefaultMax
	}
	if c.UsageEvery == 0 {
		c.UsageEvery = 250 * time.Millisecond
	}
	if c.SnapshotTimeout == 0 {
		c.SnapshotTimeout = 2 * time.Minute
	}
	if c.SnapshotRetry == 0 {
		c.SnapshotRetry = time.Second
	}
	if c.FeedRetry == 0 {
		c.FeedRetry = 20 * time.Millisecond
	}
	if c.LockRetry == 0 {
		c.LockRetry = 3 * time.Second
	}
	return nil
}

// RejectedError is a refusal with a plain-language message: the message was
// not sent, or the action does not apply. Reason is a stable code.
type RejectedError struct {
	Reason  string
	Message string
}

func (e *RejectedError) Error() string { return e.Message }

// UserMessage is the text for people.
func (e *RejectedError) UserMessage() string { return e.Message }

// Rand is the production source of randomness for worktree names
// (worktrees.Rand). It is safe for concurrent use.
type Rand struct{}

// IntN returns a random value in [0, n).
func (Rand) IntN(n int) int { return rand.IntN(n) } //nolint:gosec // names are not secrets
