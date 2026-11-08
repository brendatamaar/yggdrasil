package devservers

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Key names one server: a target (repo or worktree) and a command.
type Key struct {
	Repo       core.RepoID
	Worktree   core.WorktreeID // empty for a main checkout
	CommandKey string          // "dev", or a custom label
}

// Template is a parsed argv and env. Argv may hold {port} and {port:n}
// placeholders; Env is extra environment. ParseTemplate (shell-facing parser)
// rejects a placeholder outside the block.
type Template struct {
	Argv []string
	Env  map[string]string
}

// Expanded is a Template after expansion: argv with ports filled in and the
// environment PORT, YGG_PORT_BASE, YGG_PORT_0..n. It holds no paths.
type Expanded struct {
	Argv []string
	Env  map[string]string
}

// Port is a TCP port, 1..65535.
type Port int

// LastKind says how a server at rest last ended.
type LastKind string

// The closed set of LastKind values.
const (
	LastNever       LastKind = "never"
	LastExited      LastKind = "exited"
	LastUserStopped LastKind = "user_stopped"
	LastInterrupted LastKind = "interrupted"
)

// StopCause says why a server is stopping.
type StopCause string

// The closed set of StopCause values.
const (
	CauseUserStop    StopCause = "user_stop"
	CauseRestart     StopCause = "restart"
	CauseAppShutdown StopCause = "app_shutdown"
)

// FailKind says how a server failed.
type FailKind string

// The closed set of FailKind values.
const (
	FailStartFailed FailKind = "start_failed"
	FailExitedEarly FailKind = "exited_early"
	FailCrashed     FailKind = "crashed"
	FailStopFailed  FailKind = "stop_failed"
)

// WakeKind is the purpose of a scheduled wake.
type WakeKind string

// The closed set of WakeKind values.
const (
	WakeSettle    WakeKind = "settle"
	WakeKillGrace WakeKind = "kill_grace"
	WakePoll      WakeKind = "poll"
)

// Liveness is what the shell found for a stored live row at startup.
type Liveness interface{ isLiveness() }

// Dead means no process is left.
type Dead struct{}

// Alive means a process from a crashed run is still running (non-Windows).
type Alive struct{ Pid int }

func (Dead) isLiveness()  {}
func (Alive) isLiveness() {}

// Timing constants, in one place; tests pin the table.
const (
	SettleAfter    = 10 * time.Second
	PollStarting   = time.Second
	PollRunning    = 2 * time.Second
	KillGraceUser  = 10 * time.Second
	KillGraceClose = 5 * time.Second
)
