package devservers

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Input is one thing that happened to a server. It is a closed set. Clock
// values and attempt numbers arrive as fields; the core reads neither.
type Input interface{ isInput() }

// StartRequested asks to start. Block may be empty for a main checkout.
type StartRequested struct {
	Attempt  int
	Now      time.Time
	Template Template
	Block    core.PortBlock
}

// Started reports the process id.
type Started struct {
	Attempt int
	Pid     int
	Now     time.Time
}

// StartFailed reports that the process could not start.
type StartFailed struct {
	Attempt int
	Reason  string // plain language, no paths
	Now     time.Time
}

// PortsObserved carries the ports the OS reports for the process tree.
type PortsObserved struct {
	Attempt int
	Ports   []Port
	Now     time.Time
}

// OutputAnnounced carries ports parsed from output lines.
type OutputAnnounced struct {
	Attempt int
	Ports   []Port
}

// Wake fires a scheduled wake.
type Wake struct {
	Attempt int
	Kind    WakeKind
	Now     time.Time
}

// Exited reports that the process ended.
type Exited struct {
	Attempt int
	Code    int
	Now     time.Time
}

// StopRequested asks to stop.
type StopRequested struct {
	Cause StopCause
	Now   time.Time
}

// ShutdownRequested drains the server because Yggdrasil is closing.
type ShutdownRequested struct{ Now time.Time }

// Recovered is fed once per stored live row at startup.
type Recovered struct {
	Liveness Liveness
	Now      time.Time
}

// OrphanStopped reports that an orphaned process was stopped.
type OrphanStopped struct{ Now time.Time }

// OrphanStopFailed reports that stopping an orphaned process failed and the
// process may still be alive. The orphan stays; the failure is audited and
// published so the user sees it.
type OrphanStopFailed struct{ Now time.Time }

func (OrphanStopFailed) isInput()  {}
func (StartRequested) isInput()    {}
func (Started) isInput()           {}
func (StartFailed) isInput()       {}
func (PortsObserved) isInput()     {}
func (OutputAnnounced) isInput()   {}
func (Wake) isInput()              {}
func (Exited) isInput()            {}
func (StopRequested) isInput()     {}
func (ShutdownRequested) isInput() {}
func (Recovered) isInput()         {}
func (OrphanStopped) isInput()     {}
