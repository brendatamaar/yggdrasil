package devservers

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Server is the state of one dev server. It is a closed set: Stopped,
// Starting, Running, Stopping, Failed and Orphaned.
type Server interface {
	isServer()
	// Status is the plain string for the API and the database.
	Status() string
}

// Stopped is at rest; it may start.
type Stopped struct {
	// Attempt is the last attempt this server ran (0 if it never ran). A
	// StartRequested must carry a higher one.
	Attempt int
	Last    LastKind
	Code    int // exit code when Last is LastExited
	At      time.Time
}

// Starting has asked for the process and not yet seen it listening.
type Starting struct {
	Attempt  int
	Since    time.Time
	Block    core.PortBlock
	Expanded Expanded
	Pid      int // 0 until Started
}

// Running is alive. Ports are observed from the OS (sorted, unique);
// Announced are ports the output printed.
type Running struct {
	Attempt   int
	Since     time.Time
	Pid       int
	Block     core.PortBlock
	Ports     []Port
	Announced []Port
}

// Stopping has sent the kill. KillAttempt is 1 or 2.
type Stopping struct {
	Attempt     int
	Cause       StopCause
	KillAttempt KillAttempt
	Since       time.Time
	Pid         int
	Block       core.PortBlock
}

// Failed is at rest; it may start again.
type Failed struct {
	// Attempt is the attempt that failed.
	Attempt int
	Kind    FailKind
	Code int // exit code for FailExitedEarly and FailCrashed
	// Message is the plain-language reason (no paths), e.g. "Install Node and try again".
	Message string
	At      time.Time
}

// Orphaned is a process from a crashed run that is still alive (non-Windows).
type Orphaned struct {
	// Attempt is the attempt the process was started for.
	Attempt int
	Pid     int
	SeenAt time.Time
}

func (Stopped) isServer()  {}
func (Starting) isServer() {}
func (Running) isServer()  {}
func (Stopping) isServer() {}
func (Failed) isServer()   {}
func (Orphaned) isServer() {}

// Status implements Server.
func (Stopped) Status() string { return "stopped" }

// Status implements Server.
func (Starting) Status() string { return "starting" }

// Status implements Server.
func (Running) Status() string { return "running" }

// Status implements Server.
func (Stopping) Status() string { return "stopping" }

// Status implements Server.
func (Failed) Status() string { return "failed" }

// Status implements Server.
func (Orphaned) Status() string { return "orphaned" }
