package devservers

import (
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Row is a dev_servers row as plain values: the shape internal/db stores and
// ServerFromRow parses at the boundary. Expanded argv/env and the failure
// message are not persisted (a live row is only read at startup, where it
// becomes Stopped or Orphaned).
type Row struct {
	Key         Key
	Status      string
	Attempt     int
	Pid         int // 0 = none
	Ports       []Port
	Announced   []Port
	Block       core.PortBlock
	LastKind    LastKind
	FailKind    FailKind // "" unless failed
	ExitCode    int
	StopCause   StopCause // "" unless stopping
	KillAttempt KillAttempt
	Since       time.Time // Since, At or SeenAt; zero when none
}

// ErrBadRow is wrapped by every row rejection.
var ErrBadRow = errors.New("inconsistent dev server row")

func badRow(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRow, fmt.Sprintf(format, a...))
}

// Persisted drops what a Row does not store, so ServerFromRow(RowOf(s)) ==
// Persisted(s).
func Persisted(s Server) Server {
	switch st := s.(type) {
	case Stopped:
		if st.Last == "" {
			st.Last = LastNever
		}
		return st
	case Starting:
		st.Expanded = Expanded{}
		return st
	case Running:
		st.Ports = nonNil(st.Ports)
		st.Announced = nonNil(st.Announced)
		return st
	case Failed:
		st.Message = ""
		return st
	}
	return s
}

func nonNil(p []Port) []Port {
	if p == nil {
		return []Port{}
	}
	return p
}

// RowOf is the row of a server under key.
func RowOf(key Key, s Server) Row {
	r := Row{Key: key, Status: s.Status(), Ports: []Port{}, Announced: []Port{}, LastKind: LastNever}
	switch st := s.(type) {
	case Stopped:
		r.Attempt, r.LastKind, r.ExitCode, r.Since = st.Attempt, st.Last, st.Code, st.At
		if r.LastKind == "" {
			r.LastKind = LastNever
		}
	case Starting:
		r.Attempt, r.Pid, r.Block, r.Since = st.Attempt, st.Pid, st.Block, st.Since
	case Running:
		r.Attempt, r.Pid, r.Block, r.Since = st.Attempt, st.Pid, st.Block, st.Since
		r.Ports, r.Announced = nonNil(st.Ports), nonNil(st.Announced)
	case Stopping:
		r.Attempt, r.Pid, r.Block, r.Since = st.Attempt, st.Pid, st.Block, st.Since
		r.StopCause, r.KillAttempt = st.Cause, st.KillAttempt
	case Failed:
		r.Attempt, r.FailKind, r.ExitCode, r.Since = st.Attempt, st.Kind, st.Code, st.At
	case Orphaned:
		r.Attempt, r.Pid, r.Since = st.Attempt, st.Pid, st.SeenAt
	}
	return r
}

func validLast(k LastKind) bool {
	switch k {
	case LastNever, LastExited, LastUserStopped, LastInterrupted:
		return true
	}
	return false
}

func validFail(k FailKind) bool {
	switch k {
	case FailStartFailed, FailExitedEarly, FailCrashed, FailStopFailed:
		return true
	}
	return false
}

func validCause(c StopCause) bool {
	switch c {
	case CauseUserStop, CauseRestart, CauseAppShutdown:
		return true
	}
	return false
}

func validPorts(ps []Port) bool {
	for i, p := range ps {
		if !p.Valid() || (i > 0 && ps[i-1] >= p) {
			return false
		}
	}
	return true
}

// ServerFromRow parses a row into a Server. It rejects an unknown status, a
// state with a missing or extra field, and a live state without an attempt.
func ServerFromRow(r Row) (Server, error) {
	if r.Attempt < 0 {
		return nil, badRow("attempt %d is negative", r.Attempt)
	}
	live := r.Status == "starting" || r.Status == "running" || r.Status == "stopping"
	if (live || r.Status == "orphaned") && r.Attempt < 1 {
		return nil, badRow("%s without an attempt", r.Status)
	}
	if !r.Block.Empty() || r.Block != (core.PortBlock{}) {
		if err := r.Block.Validate(); err != nil {
			return nil, badRow("%v", err)
		}
	}
	if !validLast(r.LastKind) {
		return nil, badRow("unknown last kind %q", string(r.LastKind))
	}
	if r.Status != "stopped" && r.LastKind != LastNever {
		return nil, badRow("%s with a stopped marker", r.Status)
	}
	if r.Status != "failed" && r.FailKind != "" {
		return nil, badRow("%s with a failure kind", r.Status)
	}
	if r.Status != "stopping" && (r.StopCause != "" || r.KillAttempt != 0) {
		return nil, badRow("%s with stop data", r.Status)
	}
	if r.Status != "running" && (len(r.Ports) > 0 || len(r.Announced) > 0) {
		return nil, badRow("%s with ports", r.Status)
	}
	if !live && r.Status != "orphaned" && r.Pid != 0 {
		return nil, badRow("%s with a process id", r.Status)
	}
	if r.Pid < 0 {
		return nil, badRow("negative process id")
	}
	if r.Status != "stopped" && r.Status != "failed" && r.Since.IsZero() {
		return nil, badRow("%s without a time", r.Status)
	}
	if (r.Status == "running" || r.Status == "stopping" || r.Status == "orphaned") && r.Pid < 1 {
		return nil, badRow("%s without a process id", r.Status)
	}
	if r.Status != "starting" && r.Status != "running" && r.Status != "stopping" && !r.Block.Empty() {
		return nil, badRow("%s with a port block", r.Status)
	}
	switch r.Status {
	case "stopped":
		if r.ExitCode != 0 && r.LastKind != LastExited {
			return nil, badRow("stopped with an exit code")
		}
		return Stopped{Attempt: r.Attempt, Last: r.LastKind, Code: r.ExitCode, At: r.Since}, nil
	case "starting":
		if r.ExitCode != 0 {
			return nil, badRow("starting with an exit code")
		}
		return Starting{Attempt: r.Attempt, Since: r.Since, Block: r.Block, Pid: r.Pid}, nil
	case "running":
		if !validPorts(r.Ports) || !validPorts(r.Announced) {
			return nil, badRow("ports are not sorted, unique and within 1..65535")
		}
		if r.ExitCode != 0 {
			return nil, badRow("running with an exit code")
		}
		return Running{Attempt: r.Attempt, Since: r.Since, Pid: r.Pid, Block: r.Block, Ports: nonNil(r.Ports), Announced: nonNil(r.Announced)}, nil
	case "stopping":
		if !validCause(r.StopCause) {
			return nil, badRow("unknown stop cause %q", string(r.StopCause))
		}
		if err := r.KillAttempt.Validate(); err != nil {
			return nil, badRow("%v", err)
		}
		if r.ExitCode != 0 {
			return nil, badRow("stopping with an exit code")
		}
		return Stopping{Attempt: r.Attempt, Cause: r.StopCause, KillAttempt: r.KillAttempt, Since: r.Since, Pid: r.Pid, Block: r.Block}, nil
	case "failed":
		if !validFail(r.FailKind) {
			return nil, badRow("unknown failure kind %q", string(r.FailKind))
		}
		if r.ExitCode != 0 && r.FailKind != FailExitedEarly && r.FailKind != FailCrashed {
			return nil, badRow("failure without an exit code carries one")
		}
		return Failed{Attempt: r.Attempt, Kind: r.FailKind, Code: r.ExitCode, At: r.Since}, nil
	case "orphaned":
		if r.ExitCode != 0 {
			return nil, badRow("orphaned with an exit code")
		}
		return Orphaned{Attempt: r.Attempt, Pid: r.Pid, SeenAt: r.Since}, nil
	}
	return nil, badRow("unknown status %q", r.Status)
}
