package devservers

import "time"

// Command is an effect Step asks the shell for. It is plain data; the core
// never performs it. It is a closed set.
type Command interface{ isCommand() }

// StartProcess starts the process. Not idempotent: the shell refuses a second
// start for the same attempt. It carries argv and env, never a path (the
// shell supplies the tracked directory).
type StartProcess struct {
	Attempt  int
	Expanded Expanded
}

// KillProcess kills the server's process tree. Idempotent.
type KillProcess struct{ Attempt int }

// PollPorts reads the listening ports of the process tree.
type PollPorts struct{ Attempt int }

// ScheduleWake asks for a Wake input at At. Idempotent per (Attempt, Kind).
type ScheduleWake struct {
	Attempt int
	Kind    WakeKind
	At      time.Time
}

// StopOrphan stops an orphaned process.
type StopOrphan struct{ Pid int }

// Publish tells subscribers the server changed (after commit).
type Publish struct{}

// WriteAudit is committed with the state in the same transaction.
type WriteAudit struct {
	Action string // devserver.start, devserver.running, devserver.stop, devserver.exit, devserver.fail
	Detail map[string]string
}

// RejectReason says why a request was refused.
type RejectReason string

// The closed set of RejectReason values.
const (
	RejectAlreadyRunning RejectReason = "already_running"
	RejectBadTemplate    RejectReason = "bad_template"
)

// Reject answers a refused request (HTTP response only).
type Reject struct{ Reason RejectReason }

func (StartProcess) isCommand() {}
func (KillProcess) isCommand()  {}
func (PollPorts) isCommand()    {}
func (ScheduleWake) isCommand() {}
func (StopOrphan) isCommand()   {}
func (Publish) isCommand()      {}
func (WriteAudit) isCommand()   {}
func (Reject) isCommand()       {}
