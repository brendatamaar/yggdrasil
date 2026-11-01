package verify

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Step is (verification, input) -> (verification, commands): the transition
// table of docs/design/verify.md. An input it does not handle in the current
// state changes nothing and returns no commands. Every step that changes the
// verification returns exactly one WriteAudit and one Publish after its
// effect commands.
//
// Turn IDs are compared as strings: they must sort in the order the turns
// happened (the shell issues time-ordered IDs).
func Step(v Verification, in Input) (Verification, []Command) {
	if v.State == nil {
		return v, nil
	}
	switch in := in.(type) {
	case TurnEnded:
		return stepTurnEnded(v, in)
	case Retry:
		return stepRestart(v, restartable(v.State), in.Facts, in.Plan, in.BootBuild, in.VerifyBuild, in.Now)
	case Resume:
		i, ok := v.State.(Interrupted)
		if !ok || i.Attempt >= MaxAutoAttempts {
			return v, nil
		}
		return stepRestart(v, true, in.Facts, in.Plan, in.BootBuild, in.VerifyBuild, in.Now)
	case Cancel:
		b, live := liveBuild(v.State)
		if !live {
			return v, nil
		}
		return finish(v, Cancelled{Turn: turnOf(v.State), Cause: CancelUser, At: in.Now}, []Command{CancelBuild{Build: b}})
	case BuildFinished:
		return stepBuildFinished(v, in)
	case EnqueueFailed:
		if b, live := liveBuild(v.State); !live || b != in.Build {
			return v, nil
		}
		return finish(v, Unverifiable{Turn: turnOf(v.State), Reason: UnverifiableCouldNotQueue}, nil)
	}
	return v, nil
}

func stepTurnEnded(v Verification, in TurnEnded) (Verification, []Command) {
	if in.Turn.Validate() != nil || in.Turn <= turnOf(v.State) {
		return v, nil
	}
	next, enq, ok := v.enter(entry{
		turn: in.Turn, attempt: 1, facts: in.Facts, plan: in.Plan,
		bootBuild: in.BootBuild, verifyBuild: in.VerifyBuild, now: in.Now,
	})
	if !ok {
		return v, nil
	}
	var cmds []Command
	if old, live := liveBuild(v.State); live {
		cmds = append(cmds, CancelBuild{Build: old})
	}
	return finish(v, next, append(cmds, enq...))
}

// stepRestart handles Retry and Resume: enter again for the same turn with
// the next attempt. allowed is false for states Retry does not apply to.
func stepRestart(v Verification, allowed bool, f Facts, p Plan, boot, ver core.BuildID, now time.Time) (Verification, []Command) {
	if !allowed {
		return v, nil
	}
	next, enq, ok := v.enter(entry{
		turn: turnOf(v.State), attempt: attemptOf(v.State) + 1, facts: f, plan: p,
		bootBuild: boot, verifyBuild: ver, now: now,
	})
	if !ok {
		return v, nil
	}
	// Skipped and Unverifiable carry no attempt: entering the same outcome
	// again changes nothing. (== is safe: it only runs for these comparable
	// types, and differing dynamic types compare unequal.)
	switch next.(type) {
	case Skipped, Unverifiable:
		if v.State == next {
			return v, nil
		}
	}
	return finish(v, next, enq)
}

func stepBuildFinished(v Verification, in BuildFinished) (Verification, []Command) {
	switch s := v.State.(type) {
	case Bootstrapping:
		if in.Build != s.Build {
			return v, nil
		}
		return finishBootstrap(v, s, in)
	case Verifying:
		if in.Build != s.Build {
			return v, nil
		}
		return finishVerify(v, s, in)
	}
	return v, nil
}

func finishBootstrap(v Verification, s Bootstrapping, in BuildFinished) (Verification, []Command) {
	switch r := in.Result.(type) {
	case BuildPassed:
		if len(s.Plan.Verify) == 0 || s.NextBuild.Validate() != nil || s.NextBuild == s.Build {
			return v, nil
		}
		nv := v
		nv.BootHash = s.DepsHash
		return finish(nv, Verifying{Turn: s.Turn, Attempt: s.Attempt, Build: s.NextBuild, Plan: s.Plan, Since: in.Now},
			[]Command{EnqueueBuild{Build: s.NextBuild, Stage: StageVerify, Spec: BuildSpec{Worktree: v.Worktree, Steps: s.Plan.Verify}}})
	case BuildFailedAt, BuildStartFailedAt:
		return failed(v, s.Turn, s.Attempt, StageBootstrap, s.Build, s.Plan.Bootstrap, r, in.Now)
	case BuildCancelled:
		return finish(v, Cancelled{Turn: s.Turn, Cause: CancelExternal, At: in.Now}, nil)
	case BuildInterrupted:
		return finish(v, Interrupted{Turn: s.Turn, Attempt: s.Attempt, Stage: StageBootstrap, At: in.Now}, nil)
	}
	return v, nil
}

func finishVerify(v Verification, s Verifying, in BuildFinished) (Verification, []Command) {
	switch r := in.Result.(type) {
	case BuildPassed:
		return finish(v, Passed{Turn: s.Turn, Attempt: s.Attempt, Build: s.Build, At: in.Now}, nil)
	case BuildFailedAt, BuildStartFailedAt:
		return failed(v, s.Turn, s.Attempt, StageVerify, s.Build, s.Plan.Verify, r, in.Now)
	case BuildCancelled:
		return finish(v, Cancelled{Turn: s.Turn, Cause: CancelExternal, At: in.Now}, nil)
	case BuildInterrupted:
		return finish(v, Interrupted{Turn: s.Turn, Attempt: s.Attempt, Stage: StageVerify, At: in.Now}, nil)
	}
	return v, nil
}

// failed builds the Failed state from a failure result, or ignores the input
// when the step index is outside the chain or the reason is malformed (V13).
func failed(v Verification, turn TurnID, att Attempt, stage Stage, build core.BuildID, chain []StepSpec, res BuildResult, now time.Time) (Verification, []Command) {
	var step int
	var reason FailReason
	switch r := res.(type) {
	case BuildFailedAt:
		step, reason = r.Step, ExitCode{Code: r.ExitCode}
	case BuildStartFailedAt:
		step, reason = r.Step, StartFailed{Failure: r.Reason}
	}
	if step < 0 || step >= len(chain) || reason == nil || reason.Validate() != nil {
		return v, nil
	}
	return finish(v, Failed{Turn: turn, Attempt: att, Stage: stage, Build: build, Step: step, Reason: reason, At: now}, nil)
}

// finish sets the new state and appends the audit entry and the publish.
func finish(v Verification, next State, cmds []Command) (Verification, []Command) {
	v.State = next
	out := make([]Command, 0, len(cmds)+2)
	out = append(out, cmds...)
	return v, append(out, auditFor(next), Publish{Task: v.Task})
}

func auditFor(s State) WriteAudit {
	a := WriteAudit{Turn: turnOf(s)}
	switch s := s.(type) {
	case Skipped:
		a.Action, a.Outcome = "verify.skipped", string(s.Reason)
	case Unverifiable:
		a.Action, a.Outcome = "verify.unverifiable", string(s.Reason)
	case Bootstrapping:
		a.Action, a.Outcome = "verify.bootstrapping", "started"
	case Verifying:
		a.Action, a.Outcome = "verify.verifying", "started"
	case Passed:
		a.Action, a.Outcome = "verify.passed", "passed"
	case Failed:
		a.Action, a.Outcome = "verify.failed", string(s.Stage)
	case Interrupted:
		a.Action, a.Outcome = "verify.interrupted", string(s.Stage)
	case Cancelled:
		a.Action, a.Outcome = "verify.cancelled", string(s.Cause)
	}
	return a
}

// turnOf is the turn a state is for; None has none ("" sorts before any ID).
func turnOf(s State) TurnID {
	switch s := s.(type) {
	case Skipped:
		return s.Turn
	case Unverifiable:
		return s.Turn
	case Bootstrapping:
		return s.Turn
	case Verifying:
		return s.Turn
	case Passed:
		return s.Turn
	case Failed:
		return s.Turn
	case Interrupted:
		return s.Turn
	case Cancelled:
		return s.Turn
	}
	return ""
}

// attemptOf is the attempt a state carries; states without one report 0.
func attemptOf(s State) Attempt {
	switch s := s.(type) {
	case Bootstrapping:
		return s.Attempt
	case Verifying:
		return s.Attempt
	case Passed:
		return s.Attempt
	case Failed:
		return s.Attempt
	case Interrupted:
		return s.Attempt
	}
	return 0
}

// liveBuild is the build a Bootstrapping or Verifying state waits for.
func liveBuild(s State) (core.BuildID, bool) {
	switch s := s.(type) {
	case Bootstrapping:
		return s.Build, true
	case Verifying:
		return s.Build, true
	}
	return "", false
}

// restartable reports whether Retry applies: every state that has a turn and
// no live run.
func restartable(s State) bool {
	switch s.(type) {
	case Skipped, Unverifiable, Passed, Failed, Interrupted, Cancelled:
		return true
	}
	return false
}
