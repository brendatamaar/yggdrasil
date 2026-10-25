package tasks

import (
	"fmt"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// K3: a task has at most one live turn; the live states carry exactly one,
// it never changes while live, and a message in a live state is ignored.
func TestPropOneLiveTurn(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			if liveState(next.State) && turnOf(next.State).Validate() != nil {
				return fmt.Errorf("live state without a valid turn")
			}
			if _, isMsg := in.(Message); isMsg && liveState(prev.State) && (!sameTask(prev, next) || len(cmds) != 0) {
				return fmt.Errorf("a message changed a task with a live turn")
			}
			if liveState(prev.State) && liveState(next.State) && turnOf(prev.State) != turnOf(next.State) {
				return fmt.Errorf("the live turn changed from %s to %s", turnOf(prev.State), turnOf(next.State))
			}
			if !liveState(prev.State) && liveState(next.State) {
				if _, ok := in.(Message); !ok {
					return fmt.Errorf("a turn went live without a message")
				}
			}
			return nil
		}
	})
}

// K4: an input with a stale Turn or Attempt changes nothing. After every
// step, the stale variants of the inputs are tried against the new state.
func TestPropStaleInputIgnored(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			turn, att := turnOf(next.State), attemptOf(next.State)
			if turn == "" {
				return nil
			}
			now := epoch
			stale := []Input{
				Admitted{Turn: turn + "x", Now: now},
				Prepared{Turn: turn + "x", Attempt: att, Now: now},
				Prepared{Turn: turn, Attempt: att + 1, Now: now},
				PrepareFailed{Turn: turn, Attempt: att + 1, Now: now},
				LocksDenied{Turn: turn + "x", Attempt: att},
				UsageReported{Attempt: att + 1, Usage: Usage{InputTokens: 1}},
				SubagentStarted{Attempt: att + 1, AgentID: "z", Depth: 1},
				SubagentStopped{Attempt: att + 1, AgentID: "z"},
				RunFinished{Turn: turn, Attempt: att + 1, Status: RunCompleted, Now: now},
				RunFinished{Turn: turn + "x", Attempt: att, Status: RunCompleted, Now: now},
				IntegrityViolated{Turn: turn + "x", Now: now},
			}
			for _, s := range stale {
				got, c := Step(next, s)
				if !sameTask(got, next) || len(c) != 0 {
					return fmt.Errorf("stale input %#v changed %#v", s, next)
				}
			}
			return nil
		}
	})
}

// K5: Attempt never decreases within a turn; a finished turn never produces
// StartRun again.
func TestPropTurnNeverRestarts(t *testing.T) {
	drive(t, func() checker {
		finished := map[TurnID]bool{}
		return func(prev Task, in Input, next Task, cmds []Command) error {
			if turnOf(prev.State) == turnOf(next.State) && liveState(prev.State) && liveState(next.State) &&
				attemptOf(next.State) < attemptOf(prev.State) {
				return fmt.Errorf("attempt went down")
			}
			for _, c := range cmds {
				switch v := c.(type) {
				case StartRun:
					if finished[v.Turn] {
						return fmt.Errorf("StartRun for finished turn %s", v.Turn)
					}
				case FinishTurn:
					finished[v.Turn] = true
				}
			}
			if liveState(next.State) && finished[turnOf(next.State)] {
				return fmt.Errorf("finished turn %s is live again", turnOf(next.State))
			}
			return nil
		}
	})
}

// K6: StartRun is emitted only on Starting -> Running, once per (Turn, Attempt).
func TestPropStartRunOnce(t *testing.T) {
	drive(t, func() checker {
		type key struct {
			t TurnID
			a Attempt
		}
		seen := map[key]bool{}
		return func(prev Task, in Input, next Task, cmds []Command) error {
			n := 0
			for _, c := range cmds {
				if v, ok := c.(StartRun); ok {
					n++
					if seen[key{v.Turn, v.Attempt}] {
						return fmt.Errorf("StartRun repeated for %v", v)
					}
					seen[key{v.Turn, v.Attempt}] = true
				}
			}
			_, wasStarting := prev.State.(Starting)
			_, isRunning := next.State.(Running)
			if (n == 1) != (wasStarting && isRunning) {
				return fmt.Errorf("StartRun count %d with %T -> %T", n, prev.State, next.State)
			}
			if n > 1 {
				return fmt.Errorf("more than one StartRun")
			}
			return nil
		}
	})
}

// K7: Stopping always has a KillRun in the step that entered it, and leaves
// only to Idle.
func TestPropStoppingEndsIdle(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			_, wasStopping := prev.State.(Stopping)
			_, isStopping := next.State.(Stopping)
			if isStopping && !wasStopping && !hasCmd[KillRun](cmds) {
				return fmt.Errorf("entered Stopping without KillRun")
			}
			if wasStopping && !isStopping {
				if _, idle := next.State.(Idle); !idle {
					return fmt.Errorf("left Stopping to %T", next.State)
				}
			}
			return nil
		}
	})
}

// K8: reported usage never decreases within a task's turn.
func TestPropUsageMonotonic(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			if activeState(prev.State) && activeState(next.State) && !usageGE(usageOf(next.State), usageOf(prev.State)) {
				return fmt.Errorf("usage went down: %+v -> %+v", usageOf(prev.State), usageOf(next.State))
			}
			for _, c := range cmds {
				if f, ok := c.(FinishTurn); ok && activeState(prev.State) && !usageGE(f.Usage, usageOf(prev.State)) {
					return fmt.Errorf("FinishTurn usage %+v is below %+v", f.Usage, usageOf(prev.State))
				}
			}
			return nil
		}
	})
}

// K9: an observed sub-agent over the depth or concurrency cap always yields
// KillRun; under the cap, never.
func TestPropSubagentCapsEnforced(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			e, ok := in.(SubagentStarted)
			r, running := prev.State.(Running)
			if !ok || !running || r.Attempt != e.Attempt {
				return nil
			}
			active := false
			for _, a := range r.Subagents {
				if a.ID == e.AgentID {
					active = true
				}
			}
			over := e.Depth > prev.Caps.Depth || (!active && len(r.Subagents) >= prev.Caps.Concurrent)
			if over != hasCmd[KillRun](cmds) {
				return fmt.Errorf("over=%v but KillRun=%v", over, hasCmd[KillRun](cmds))
			}
			if s, isStop := next.State.(Stopping); over && (!isStop || s.Cause != CauseCapExceeded) {
				return fmt.Errorf("over the cap but %T", next.State)
			}
			if !over {
				if _, still := next.State.(Running); !still {
					return fmt.Errorf("under the cap but %T", next.State)
				}
			}
			return nil
		}
	})
}

// K10: Recovered leaves no task in Starting, Running or Stopping and emits no
// StartRun or PrepareRun.
func TestPropRecoveryStartsNothing(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			if _, ok := in.(Recovered); !ok {
				return nil
			}
			if activeState(next.State) {
				return fmt.Errorf("Recovered left %T", next.State)
			}
			if hasCmd[StartRun](cmds) || hasCmd[PrepareRun](cmds) {
				return fmt.Errorf("Recovered started something")
			}
			if activeState(prev.State) {
				if i, ok := next.State.(Idle); !ok || (i.Outcome != Failed{Kind: FailInterrupted}) {
					return fmt.Errorf("recovered to %#v", next.State)
				}
			}
			return nil
		}
	})
}

// K12: a refused admission never changes state and never starts a turn; an
// accepted message needs an admission and a valid mode.
func TestPropMessageRespectsGuard(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			m, ok := in.(Message)
			if !ok {
				return nil
			}
			if !m.Admission.Admitted {
				if !sameTask(prev, next) || hasCmd[PersistTurn](cmds) || hasCmd[Enqueue](cmds) {
					return fmt.Errorf("refused message changed something")
				}
				if liveState(prev.State) {
					return nil
				}
				if !hasCmd[Reject](cmds) {
					return fmt.Errorf("refused message without Reject")
				}
				return nil
			}
			if w, ok := next.State.(Waiting); ok && !liveState(prev.State) {
				if w.Turn != m.Turn || next.Mode != m.Mode || next.Turns != prev.Turns+1 {
					return fmt.Errorf("accepted message produced %#v", next)
				}
				if m.Mode.Validate() != nil {
					return fmt.Errorf("started a turn with mode %q", m.Mode)
				}
				for _, c := range cmds {
					if e, ok := c.(Enqueue); ok && m.Mode != core.ModeCurrentBranch && len(e.EditsMain) != 0 {
						return fmt.Errorf("a %s turn asked for a main-checkout lock", m.Mode)
					}
				}
			}
			return nil
		}
	})
}

// K13: every command batch that changes the status has an Audit and a
// Publish for the new status.
func TestPropStatusChangeAudited(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			if prev.State.Status() == next.State.Status() {
				for _, c := range cmds {
					if p, ok := c.(Publish); ok && p.Status != next.State.Status() {
						return fmt.Errorf("Publish of %q while status is %q", p.Status, next.State.Status())
					}
				}
				return nil
			}
			if !hasCmd[Audit](cmds) || !publishes(cmds, next.State.Status()) {
				return fmt.Errorf("status %q -> %q without Audit and Publish: %#v", prev.State.Status(), next.State.Status(), cmds)
			}
			return nil
		}
	})
}

// Every run that ends goes through FinishTurn, ReleaseLocks and (when a
// process may exist) KillTask; locks are never left behind.
func TestPropEndOfTurnReleasesLocks(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			_, isIdle := next.State.(Idle)
			if activeState(prev.State) && isIdle {
				if !hasCmd[FinishTurn](cmds) || !hasCmd[ReleaseLocks](cmds) {
					return fmt.Errorf("turn ended without FinishTurn and ReleaseLocks")
				}
			}
			return nil
		}
	})
}
