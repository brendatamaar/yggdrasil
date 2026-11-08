package devservers

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Step is the dev-server state machine: (state2, commands) = Step(state1,
// input). An input that is illegal for the state returns the state unchanged
// and no commands (docs/design/devservers.md). It never changes its input.
func Step(s Server, in Input) (Server, []Command) {
	switch st := s.(type) {
	case Stopped:
		return stepAtRest(s, st.Attempt, in)
	case Failed:
		return stepAtRest(s, st.Attempt, in)
	case Starting:
		return stepStarting(st, in)
	case Running:
		return stepRunning(st, in)
	case Stopping:
		return stepStopping(st, in)
	case Orphaned:
		return stepOrphaned(st, in)
	}
	return s, nil
}

// Run feeds inputs to Step in order and returns the final state and every
// command emitted, in order. The property tests drive Step through it.
func Run(s Server, inputs []Input) (Server, []Command) {
	var all []Command
	for _, in := range inputs {
		var cmds []Command
		s, cmds = Step(s, in)
		all = append(all, cmds...)
	}
	return s, all
}

func detail(kv ...string) map[string]string {
	m := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func itoa(n int) string { return strconv.Itoa(n) }

func portsText(ps []Port) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = strconv.Itoa(int(p))
	}
	return strings.Join(parts, ",")
}

func audit(a AuditAction, kv ...string) Command { return WriteAudit{Action: a, Detail: detail(kv...)} }

func wake(attempt int, kind WakeKind, now time.Time, after time.Duration) Command {
	return ScheduleWake{Attempt: attempt, Kind: kind, At: now.Add(after)}
}

func killGrace(c StopCause) time.Duration {
	if c == CauseAppShutdown {
		return KillGraceClose
	}
	return KillGraceUser
}

// stepAtRest handles Stopped and Failed. last is the last attempt that ran.
func stepAtRest(s Server, last int, in Input) (Server, []Command) {
	req, ok := in.(StartRequested)
	if !ok || req.Attempt < 1 || req.Attempt <= last {
		return s, nil
	}
	if !req.Block.Empty() && req.Block.Validate() != nil {
		return s, []Command{Reject{Reason: RejectBadTemplate, Message: "The worktree's port block is not valid."}}
	}
	exp, err := Expand(req.Template, req.Block)
	if err != nil {
		return s, []Command{Reject{Reason: RejectBadTemplate, Message: err.Error()}}
	}
	return Starting{Attempt: req.Attempt, Since: req.Now, Block: req.Block, Expanded: exp}, []Command{
		StartProcess{Attempt: req.Attempt, Expanded: exp},
		wake(req.Attempt, WakeSettle, req.Now, SettleAfter),
		audit(AuditStart, "attempt", itoa(req.Attempt)),
		Publish{},
	}
}

func rejectRunning() []Command { return []Command{Reject{Reason: RejectAlreadyRunning, Message: "This server is already running or stopping."}} }

func stepStarting(st Starting, in Input) (Server, []Command) {
	switch v := in.(type) {
	case StartRequested:
		return st, rejectRunning()
	case Started:
		if v.Attempt != st.Attempt || st.Pid != 0 || v.Pid < 1 {
			return st, nil
		}
		st.Pid = v.Pid
		return st, []Command{wake(st.Attempt, WakePoll, v.Now, PollStarting)}
	case StartFailed:
		if v.Attempt != st.Attempt {
			return st, nil
		}
		return Failed{Attempt: st.Attempt, Kind: FailStartFailed, Message: v.Reason, At: v.Now}, []Command{
			audit(AuditFail, "attempt", itoa(st.Attempt), "kind", string(FailStartFailed)), Publish{},
		}
	case PortsObserved:
		ports := normalizeValid(v.Ports)
		if v.Attempt != st.Attempt || st.Pid == 0 || len(ports) == 0 {
			return st, nil
		}
		return running(st, ports), []Command{
			wake(st.Attempt, WakePoll, v.Now, PollRunning),
			audit(AuditRunning, "attempt", itoa(st.Attempt), "ports", portsText(ports)), Publish{},
		}
	case Wake:
		if v.Attempt != st.Attempt || st.Pid == 0 {
			return st, nil
		}
		switch v.Kind {
		case WakeSettle:
			return running(st, []Port{}), []Command{
				wake(st.Attempt, WakePoll, v.Now, PollRunning),
				audit(AuditRunning, "attempt", itoa(st.Attempt), "ports", ""), Publish{},
			}
		case WakePoll:
			return st, []Command{PollPorts{Attempt: st.Attempt}, wake(st.Attempt, WakePoll, v.Now, PollStarting)}
		}
		return st, nil
	case Exited:
		if v.Attempt != st.Attempt {
			return st, nil
		}
		return Failed{
				Attempt: st.Attempt, Kind: FailExitedEarly, Code: v.Code, At: v.Now,
				Message: "The server stopped before it started listening (exit code " + itoa(v.Code) + ").",
			}, []Command{
				audit(AuditFail, "attempt", itoa(st.Attempt), "kind", string(FailExitedEarly), "code", itoa(v.Code)), Publish{},
			}
	case StopRequested:
		if st.Pid == 0 {
			// The process may not exist yet: nothing to wait for. The kill
			// cancels the attempt so a late start is stopped too.
			return Stopped{Attempt: st.Attempt, Last: stoppedKind(v.Cause), At: v.Now}, []Command{
				KillProcess{Attempt: st.Attempt}, audit(AuditStop, "attempt", itoa(st.Attempt), "cause", string(v.Cause)), Publish{},
			}
		}
		return stopping(st.Attempt, st.Pid, st.Block, v.Cause, v.Now)
	case ShutdownRequested:
		if st.Pid == 0 {
			return Stopped{Attempt: st.Attempt, Last: LastInterrupted, At: v.Now}, []Command{
				KillProcess{Attempt: st.Attempt}, audit(AuditStop, "attempt", itoa(st.Attempt), "cause", string(CauseAppShutdown)), Publish{},
			}
		}
		return stopping(st.Attempt, st.Pid, st.Block, CauseAppShutdown, v.Now)
	case Recovered:
		return recovered(st.Attempt, st.Block, v)
	}
	return st, nil
}

func stoppedKind(c StopCause) LastKind {
	if c == CauseAppShutdown {
		return LastInterrupted
	}
	return LastUserStopped
}

func running(st Starting, ports []Port) Running {
	return Running{Attempt: st.Attempt, Since: st.Since, Pid: st.Pid, Block: st.Block, Ports: ports, Announced: []Port{}}
}

func stopping(attempt, pid int, block core.PortBlock, c StopCause, now time.Time) (Server, []Command) {
	return Stopping{Attempt: attempt, Cause: c, KillAttempt: KillFirst, Since: now, Pid: pid, Block: block}, []Command{
		KillProcess{Attempt: attempt},
		wake(attempt, WakeKillGrace, now, killGrace(c)),
		audit(AuditStop, "attempt", itoa(attempt), "cause", string(c)),
		Publish{},
	}
}

func stepRunning(st Running, in Input) (Server, []Command) {
	switch v := in.(type) {
	case StartRequested:
		return st, rejectRunning()
	case Wake:
		if v.Attempt != st.Attempt || v.Kind != WakePoll {
			return st, nil
		}
		return st, []Command{PollPorts{Attempt: st.Attempt}, wake(st.Attempt, WakePoll, v.Now, PollRunning)}
	case PortsObserved:
		ports := normalizeValid(v.Ports)
		if v.Attempt != st.Attempt || slices.Equal(ports, st.Ports) {
			return st, nil
		}
		st.Ports = ports
		return st, []Command{Publish{}}
	case OutputAnnounced:
		ports := normalizeValid(v.Ports)
		if v.Attempt != st.Attempt || slices.Equal(ports, st.Announced) {
			return st, nil
		}
		st.Announced = ports
		return st, []Command{Publish{}}
	case Exited:
		if v.Attempt != st.Attempt {
			return st, nil
		}
		if v.Code == 0 {
			return Stopped{Attempt: st.Attempt, Last: LastExited, Code: 0, At: v.Now}, []Command{
				audit(AuditExit, "attempt", itoa(st.Attempt), "code", "0"), Publish{},
			}
		}
		return Failed{
				Attempt: st.Attempt, Kind: FailCrashed, Code: v.Code, At: v.Now,
				Message: "The server stopped unexpectedly (exit code " + itoa(v.Code) + ").",
			}, []Command{
				audit(AuditFail, "attempt", itoa(st.Attempt), "kind", string(FailCrashed), "code", itoa(v.Code)), Publish{},
			}
	case StopRequested:
		return stopping(st.Attempt, st.Pid, st.Block, v.Cause, v.Now)
	case ShutdownRequested:
		return stopping(st.Attempt, st.Pid, st.Block, CauseAppShutdown, v.Now)
	case Recovered:
		return recovered(st.Attempt, st.Block, v)
	}
	return st, nil
}

func stepStopping(st Stopping, in Input) (Server, []Command) {
	switch v := in.(type) {
	case StartRequested:
		return st, rejectRunning()
	case Exited:
		if v.Attempt != st.Attempt {
			return st, nil
		}
		return Stopped{Attempt: st.Attempt, Last: stoppedKind(st.Cause), At: v.Now}, []Command{
			audit(AuditExit, "attempt", itoa(st.Attempt), "code", itoa(v.Code), "cause", string(st.Cause)), Publish{},
		}
	case Wake:
		if v.Attempt != st.Attempt || v.Kind != WakeKillGrace {
			return st, nil
		}
		if st.KillAttempt == KillFirst {
			st.KillAttempt = KillSecond
			return st, []Command{KillProcess{Attempt: st.Attempt}, wake(st.Attempt, WakeKillGrace, v.Now, killGrace(st.Cause))}
		}
		return Failed{
				Attempt: st.Attempt, Kind: FailStopFailed, At: v.Now,
				Message: "The server did not stop. It ends when Yggdrasil closes.",
			}, []Command{
				audit(AuditFail, "attempt", itoa(st.Attempt), "kind", string(FailStopFailed)), Publish{},
			}
	case Recovered:
		return recovered(st.Attempt, st.Block, v)
	}
	return st, nil
}

func stepOrphaned(st Orphaned, in Input) (Server, []Command) {
	switch v := in.(type) {
	case StartRequested:
		return st, rejectRunning()
	case StopRequested:
		return st, []Command{StopOrphan{Pid: st.Pid}, audit(AuditStop, "attempt", itoa(st.Attempt), "pid", itoa(st.Pid))}
	case OrphanStopped:
		return Stopped{Attempt: st.Attempt, Last: LastInterrupted, At: v.Now}, []Command{
			audit(AuditExit, "attempt", itoa(st.Attempt), "orphan", "true"), Publish{},
		}
	}
	return st, nil
}

// recovered handles a stored live row at startup.
func recovered(attempt int, _ core.PortBlock, v Recovered) (Server, []Command) {
	if a, ok := v.Liveness.(Alive); ok && a.Pid >= 1 {
		return Orphaned{Attempt: attempt, Pid: a.Pid, SeenAt: v.Now}, []Command{
			audit(AuditOrphaned, "attempt", itoa(attempt), "pid", itoa(a.Pid)), Publish{},
		}
	}
	return Stopped{Attempt: attempt, Last: LastInterrupted, At: v.Now}, []Command{
		audit(AuditRecovered, "attempt", itoa(attempt)), Publish{},
	}
}
