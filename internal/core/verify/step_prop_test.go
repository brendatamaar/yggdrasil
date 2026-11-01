package verify

import (
	"fmt"
	"math/rand"
	"testing"
)

type checkFn = func(t *testing.T, before Verification, in Input, after Verification, cmds []Command)

func prop(t *testing.T, check checkFn) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) { run(t, r, check) })
}

// V1: every transition into a live state emits exactly one EnqueueBuild with
// the state's build and stage; no other transition emits one.
func TestPropOneEnqueuePerLiveEntry(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		n := count[EnqueueBuild](cmds)
		if !changed(before, after) || !isLiveState(after.State) {
			if n != 0 {
				t.Errorf("%d EnqueueBuild without entering a live state", n)
			}
			return
		}
		if n != 1 {
			t.Fatalf("%d EnqueueBuild entering %T", n, after.State)
		}
		for _, c := range cmds {
			e, ok := c.(EnqueueBuild)
			if !ok {
				continue
			}
			want, _ := liveBuild(after.State)
			if e.Build != want {
				t.Errorf("enqueued %s, state waits for %s", e.Build, want)
			}
			if _, boot := after.State.(Bootstrapping); boot != (e.Stage == StageBootstrap) {
				t.Errorf("stage %s for %T", e.Stage, after.State)
			}
			if e.Spec.Worktree != after.Worktree || e.Spec.Validate() != nil {
				t.Errorf("bad spec %+v", e.Spec)
			}
		}
	})
}

// V2: a BuildFinished for a build that is not the current one changes nothing.
func TestPropStaleBuildIgnored(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		bf, ok := in.(BuildFinished)
		if !ok {
			return
		}
		if cur, live := liveBuild(before.State); live && cur == bf.Build {
			return
		}
		if changed(before, after) || len(cmds) != 0 {
			t.Errorf("stale BuildFinished %s changed %T or emitted %d commands", bf.Build, before.State, len(cmds))
		}
	})
}

// V3: Passed is only reached from Verifying by BuildPassed of its build.
func TestPropPassedOnlyFromVerify(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if _, ok := after.State.(Passed); !ok || !changed(before, after) {
			return
		}
		vs, ok := before.State.(Verifying)
		bf, isBF := in.(BuildFinished)
		if !ok || !isBF || bf.Build != vs.Build {
			t.Fatalf("Passed reached from %T by %T", before.State, in)
		}
		if _, passed := bf.Result.(BuildPassed); !passed {
			t.Errorf("Passed reached by %T", bf.Result)
		}
	})
}

// V4: the turn never decreases, and a TurnEnded that is not newer changes
// nothing.
func TestPropTurnMonotonic(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if seqOf(after.State) < seqOf(before.State) {
			t.Errorf("turn seq went from %d to %d", seqOf(before.State), seqOf(after.State))
		}
		if te, ok := in.(TurnEnded); ok && te.Seq <= seqOf(before.State) && (changed(before, after) || len(cmds) != 0) {
			t.Errorf("TurnEnded seq %d not newer than %d had an effect", te.Seq, seqOf(before.State))
		}
	})
}

// V5: the attempt never decreases within a turn; Resume moves only an
// Interrupted state below MaxAutoAttempts, by one; Retry is the way past.
func TestPropAttemptRules(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if seqOf(before.State) == seqOf(after.State) {
			a0, a1 := attemptOf(before.State), attemptOf(after.State)
			if a0 > 0 && a1 > 0 && a1 < a0 {
				t.Errorf("attempt went from %d to %d", a0, a1)
			}
		}
		switch in.(type) {
		case Resume:
			i, ok := before.State.(Interrupted)
			if !ok || i.Attempt >= MaxAutoAttempts {
				if changed(before, after) {
					t.Errorf("Resume changed %T", before.State)
				}
				return
			}
			if a := attemptOf(after.State); a != 0 && changed(before, after) && a != i.Attempt+1 {
				t.Errorf("Resume attempt %d -> %d", i.Attempt, a)
			}
		case Retry:
			if a := attemptOf(after.State); a != 0 && changed(before, after) && isTerminal(before.State) && a != attemptOf(before.State)+1 {
				t.Errorf("Retry attempt %d -> %d", attemptOf(before.State), a)
			}
		}
	})
}

// V6: BootHash changes only on bootstrap success, to that bootstrap's hash.
func TestPropBootHashOnlyOnSuccess(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if before.BootHash == after.BootHash {
			return
		}
		bs, ok := before.State.(Bootstrapping)
		bf, isBF := in.(BuildFinished)
		if !ok || !isBF || bf.Build != bs.Build {
			t.Fatalf("BootHash changed from %T by %T", before.State, in)
		}
		if _, passed := bf.Result.(BuildPassed); !passed {
			t.Errorf("BootHash changed by %T", bf.Result)
		}
		if after.BootHash != bs.DepsHash {
			t.Errorf("BootHash %q, bootstrap was for %q", after.BootHash, bs.DepsHash)
		}
	})
}

// V7: a bootstrap starts only when the plan has bootstrap steps and its hash
// differs from BootHash; otherwise the first live state is Verifying.
func TestPropBootstrapOnlyWhenHashChanged(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		var p Plan
		switch in := in.(type) {
		case TurnEnded:
			p = in.Plan
		case Retry:
			p = in.Plan
		case Resume:
			p = in.Plan
		default:
			return
		}
		if !changed(before, after) || !isLiveState(after.State) {
			return
		}
		need := len(p.Bootstrap) > 0 && p.DepsHash != before.BootHash
		_, boot := after.State.(Bootstrapping)
		if boot != need {
			t.Errorf("bootstrap=%v, need=%v (steps %d, hash %q, boot hash %q)", boot, need, len(p.Bootstrap), p.DepsHash, before.BootHash)
		}
	})
}

// V8: terminal states change only through a newer TurnEnded, Retry, or Resume
// of an Interrupted state.
func TestPropTerminalOnlyLeavesByRestartInputs(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if !isTerminal(before.State) || !changed(before, after) {
			return
		}
		switch in := in.(type) {
		case TurnEnded:
			if in.Seq <= seqOf(before.State) {
				t.Errorf("terminal %T left by a TurnEnded that is not newer", before.State)
			}
		case Retry:
		case Resume:
			if _, ok := before.State.(Interrupted); !ok {
				t.Errorf("Resume moved %T", before.State)
			}
		default:
			t.Errorf("terminal %T left by %T", before.State, in)
		}
	})
}

// V9: a live state never has an empty chain, and every state is valid.
func TestPropLiveStatesHaveChains(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if err := after.Validate(); err != nil {
			t.Errorf("invalid aggregate: %v", err)
		}
		switch s := after.State.(type) {
		case Verifying:
			if len(s.Plan.Verify) == 0 {
				t.Error("Verifying with an empty verify chain")
			}
		case Bootstrapping:
			if len(s.Plan.Bootstrap) == 0 {
				t.Error("Bootstrapping with an empty bootstrap chain")
			}
		}
	})
}

// V10: Skipped and Unverifiable emit no build to start.
func TestPropNoBuildForSkipped(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		switch after.State.(type) {
		case Skipped, Unverifiable:
			if n := count[EnqueueBuild](cmds); n != 0 {
				t.Errorf("%d EnqueueBuild entering %T", n, after.State)
			}
		}
	})
}

// V11: a changing step emits exactly one WriteAudit and one Publish.
func TestPropAuditPerChange(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if !changed(before, after) {
			return
		}
		if a, p := count[WriteAudit](cmds), count[Publish](cmds); a != 1 || p != 1 {
			t.Errorf("%T -> %T: %d audits, %d publishes", before.State, after.State, a, p)
		}
		for _, c := range cmds {
			if a, ok := c.(WriteAudit); ok && (a.Action == "" || a.Turn != turnOf(after.State)) {
				t.Errorf("bad audit %+v", a)
			}
		}
	})
}

// V11: a step that changes nothing emits no command.
func TestPropNoChangeNoCommands(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if !changed(before, after) && len(cmds) != 0 {
			t.Errorf("%T on %T changed nothing but emitted %d commands", in, before.State, len(cmds))
		}
	})
}

// V12: Task, Repo, Worktree (and ID) never change.
func TestPropIdentityStable(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		if before.ID != after.ID || before.Task != after.Task || before.Repo != after.Repo || before.Worktree != after.Worktree {
			t.Errorf("identity changed: %+v -> %+v", before, after)
		}
	})
}

// V13: the failing step is inside the chain of its stage, or the input is
// ignored.
func TestPropFailedStepInRange(t *testing.T) {
	prop(t, func(t *testing.T, before Verification, in Input, after Verification, cmds []Command) {
		bf, isBF := in.(BuildFinished)
		var chain int
		switch s := before.State.(type) {
		case Bootstrapping:
			chain = len(s.Plan.Bootstrap)
		case Verifying:
			chain = len(s.Plan.Verify)
		default:
			return
		}
		if cur, _ := liveBuild(before.State); !isBF || bf.Build != cur {
			return
		}
		var step int
		switch r := bf.Result.(type) {
		case BuildFailedAt:
			step = r.Step
		case BuildStartFailedAt:
			step = r.Step
		default:
			return
		}
		f, isFailed := after.State.(Failed)
		if step < 0 || step >= chain {
			if changed(before, after) {
				t.Errorf("step %d of %d was accepted", step, chain)
			}
			return
		}
		if isFailed && (f.Step != step || f.Step >= chain) {
			t.Errorf("Failed.Step %d, input %d, chain %d", f.Step, step, chain)
		}
	})
}

// The generator must reach every state, or the properties prove little.
func TestGeneratorCoversStates(t *testing.T) {
	seen := map[string]bool{}
	for seed := int64(1); seed <= propRuns; seed++ {
		run(t, rand.New(rand.NewSource(seed)), func(_ *testing.T, _ Verification, _ Input, after Verification, _ []Command) {
			seen[fmt.Sprintf("%T", after.State)] = true
		})
	}
	for _, want := range []State{Bootstrapping{}, Verifying{}, Passed{}, Failed{}, Interrupted{}, Cancelled{}, Skipped{}, Unverifiable{}} {
		if !seen[fmt.Sprintf("%T", want)] {
			t.Errorf("generator never reached %T", want)
		}
	}
}
