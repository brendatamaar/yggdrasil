package verify

import (
	"reflect"
	"testing"
)

var (
	okFacts  = Facts{Enabled: true, Trusted: true, WorktreeActive: true, HasChanges: true}
	bootStep = []StepSpec{{Name: "install", Argv: []string{"npm", "ci"}}}
	chkSteps = []StepSpec{{Name: "lint", Argv: []string{"npm", "run", "lint"}}, {Name: "test", Argv: []string{"npm", "test"}}}
	planBoot = Plan{Bootstrap: bootStep, Verify: chkSteps, DepsHash: "h1"}
	planPlan = Plan{Verify: chkSteps, DepsHash: "h1"}
)

func agg(s State) Verification {
	v := newAggregate()
	v.State = s
	return v
}

func turnEnded(f Facts, p Plan) TurnEnded {
	return TurnEnded{Turn: "t1", Seq: 1, Facts: f, Plan: p, BootBuild: "boot1", VerifyBuild: "ver1", Now: epoch}
}

func TestEnterOrder(t *testing.T) {
	with := func(f func(*Facts)) Facts { x := okFacts; f(&x); return x }
	none := Plan{DepsHash: "h"}
	tests := []struct {
		name  string
		facts Facts
		plan  Plan
		want  State
	}{
		{"disabled wins over everything", Facts{}, none, Skipped{Turn: "t1", TurnSeq: 1, Reason: SkipDisabled}},
		{"no worktree before trust", with(func(f *Facts) { f.WorktreeActive, f.Trusted = false, false }), planBoot, Unverifiable{Turn: "t1", TurnSeq: 1, Attempt: 1, Reason: UnverifiableWorktreeMissing}},
		{"not trusted before no changes", with(func(f *Facts) { f.Trusted, f.HasChanges = false, false }), planBoot, Unverifiable{Turn: "t1", TurnSeq: 1, Attempt: 1, Reason: UnverifiableNotTrusted}},
		{"no changes before no command", with(func(f *Facts) { f.HasChanges = false }), none, Skipped{Turn: "t1", TurnSeq: 1, Reason: SkipNoChanges}},
		{"no command", okFacts, Plan{Bootstrap: bootStep, DepsHash: "h"}, Unverifiable{Turn: "t1", TurnSeq: 1, Attempt: 1, Reason: UnverifiableNoCommand}},
		{"bootstrap when hash differs", okFacts, planBoot, Bootstrapping{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "boot1", Plan: planBoot, NextBuild: "ver1", DepsHash: "h1", Since: epoch}},
		{"verify when there is nothing to bootstrap", okFacts, planPlan, Verifying{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "ver1", Plan: planPlan, Since: epoch}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, cmds := Step(agg(None{}), turnEnded(tc.facts, tc.plan))
			if !reflect.DeepEqual(got.State, tc.want) {
				t.Fatalf("state = %#v, want %#v", got.State, tc.want)
			}
			if len(cmds) < 2 {
				t.Fatalf("commands = %v, want audit and publish at least", cmds)
			}
		})
	}
}

func TestEnterSkipsBootstrapWhenHashKnown(t *testing.T) {
	v := agg(None{})
	v.BootHash = "h1"
	got, _ := Step(v, turnEnded(okFacts, planBoot))
	if _, ok := got.State.(Verifying); !ok {
		t.Fatalf("state = %T, want Verifying", got.State)
	}
}

func TestEnterRejectsBadData(t *testing.T) {
	bad := []TurnEnded{
		turnEnded(okFacts, Plan{Verify: []StepSpec{{Name: "", Argv: []string{"x"}}}}),
		{Turn: "", Seq: 1, Facts: okFacts, Plan: planPlan, VerifyBuild: "v", Now: epoch},
		{Turn: "t1", Seq: 0, Facts: okFacts, Plan: planPlan, VerifyBuild: "v", Now: epoch},
		{Turn: "t1", Seq: -3, Facts: okFacts, Plan: planPlan, VerifyBuild: "v", Now: epoch},
		{Turn: "t1", Seq: 1, Facts: okFacts, Plan: planPlan, VerifyBuild: "", Now: epoch},
		{Turn: "t1", Seq: 1, Facts: okFacts, Plan: planBoot, BootBuild: "", VerifyBuild: "v", Now: epoch},
		{Turn: "t1", Seq: 1, Facts: okFacts, Plan: planBoot, BootBuild: "v", VerifyBuild: "v", Now: epoch},
	}
	for i, in := range bad {
		got, cmds := Step(agg(None{}), in)
		if _, ok := got.State.(None); !ok || len(cmds) != 0 {
			t.Errorf("case %d: accepted (%T, %d commands)", i, got.State, len(cmds))
		}
	}
}

func TestSupersedeCancelsOldBuild(t *testing.T) {
	old := Verifying{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "old", Plan: planPlan, Since: epoch}
	in := turnEnded(okFacts, planPlan)
	in.Turn, in.Seq = "t2", 2
	got, cmds := Step(agg(old), in)
	if _, ok := got.State.(Verifying); !ok || seqOf(got.State) != 2 {
		t.Fatalf("state = %#v", got.State)
	}
	if len(cmds) != 4 || cmds[0] != (CancelBuild{Build: "old"}) {
		t.Fatalf("commands = %#v, want CancelBuild(old) first", cmds)
	}
	// A terminal old state has nothing to cancel.
	_, cmds = Step(agg(Passed{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "old"}), in)
	if count[CancelBuild](cmds) != 0 {
		t.Fatal("cancelled a finished build")
	}
	// Same or older turn: nothing.
	in.Turn, in.Seq = "t1", 1
	if got, cmds := Step(agg(old), in); changed(got, agg(old)) || len(cmds) != 0 {
		t.Fatal("a turn that is not newer had an effect")
	}
}

func TestRetryAndResume(t *testing.T) {
	retry := Retry{Facts: okFacts, Plan: planPlan, BootBuild: "boot2", VerifyBuild: "ver2", Now: epoch}
	resume := Resume{Facts: okFacts, Plan: planPlan, BootBuild: "boot2", VerifyBuild: "ver2", Now: epoch}
	live := Verifying{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "ver1", Plan: planPlan}
	if got, cmds := Step(agg(live), retry); changed(got, agg(live)) || len(cmds) != 0 {
		t.Error("Retry changed a live run")
	}
	if got, _ := Step(agg(None{}), retry); changed(got, agg(None{})) {
		t.Error("Retry changed None")
	}
	f := Failed{Turn: "t1", TurnSeq: 1, Attempt: 2, Stage: StageVerify, Build: "ver1", Step: 0, Reason: ExitCode{Code: 1}}
	if got, _ := Step(agg(f), retry); !reflect.DeepEqual(got.State, Verifying{Turn: "t1", TurnSeq: 1, Attempt: 3, Build: "ver2", Plan: planPlan, Since: epoch}) {
		t.Errorf("Retry from Failed = %#v", got.State)
	}
	i1 := Interrupted{Turn: "t1", TurnSeq: 1, Attempt: 1, Stage: StageVerify}
	if got, _ := Step(agg(i1), resume); attemptOf(got.State) != 2 {
		t.Errorf("Resume from first Interrupted = %#v", got.State)
	}
	i2 := Interrupted{Turn: "t1", TurnSeq: 1, Attempt: MaxAutoAttempts, Stage: StageVerify}
	if got, cmds := Step(agg(i2), resume); changed(got, agg(i2)) || len(cmds) != 0 {
		t.Error("Resume went past MaxAutoAttempts")
	}
	if got, _ := Step(agg(i2), retry); attemptOf(got.State) != MaxAutoAttempts+1 {
		t.Error("Retry did not go past MaxAutoAttempts")
	}
	if got, _ := Step(agg(f), resume); changed(got, agg(f)) {
		t.Error("Resume changed Failed")
	}
	sk := Skipped{Turn: "t1", TurnSeq: 1, Reason: SkipNoChanges}
	noChange := retry
	noChange.Facts.HasChanges = false
	if got, cmds := Step(agg(sk), noChange); changed(got, agg(sk)) || len(cmds) != 0 {
		t.Error("Retry into the same Skipped outcome had an effect")
	}
}

func TestBuildFinishedRows(t *testing.T) {
	boot := Bootstrapping{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "boot1", Plan: planBoot, NextBuild: "ver1", DepsHash: "h1", Since: epoch}
	ver := Verifying{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "ver1", Plan: planBoot, Since: epoch}
	fin := func(b string, r BuildResult) BuildFinished {
		return BuildFinished{Build: toBuild(b), Result: r, Now: epoch}
	}
	tests := []struct {
		name string
		from State
		in   BuildFinished
		want State
		hash string
	}{
		{"bootstrap passed hops to verify", boot, fin("boot1", BuildPassed{}), Verifying{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "ver1", Plan: planBoot, Since: epoch}, "h1"},
		{"bootstrap failed", boot, fin("boot1", BuildFailedAt{Step: 0, ExitCode: 2}), Failed{Turn: "t1", TurnSeq: 1, Attempt: 1, Stage: StageBootstrap, Build: "boot1", Step: 0, Reason: ExitCode{Code: 2}, At: epoch}, ""},
		{"bootstrap did not start", boot, fin("boot1", BuildStartFailedAt{Step: 0, Reason: "npm was not found"}), Failed{Turn: "t1", TurnSeq: 1, Attempt: 1, Stage: StageBootstrap, Build: "boot1", Step: 0, Reason: StartFailed{Failure: "npm was not found"}, At: epoch}, ""},
		{"bootstrap cancelled", boot, fin("boot1", BuildCancelled{}), Cancelled{Turn: "t1", TurnSeq: 1, Attempt: 1, Cause: CancelExternal, At: epoch}, ""},
		{"bootstrap interrupted", boot, fin("boot1", BuildInterrupted{}), Interrupted{Turn: "t1", TurnSeq: 1, Attempt: 1, Stage: StageBootstrap, At: epoch}, ""},
		{"verify passed", ver, fin("ver1", BuildPassed{}), Passed{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "ver1", At: epoch}, ""},
		{"verify failed at step 1", ver, fin("ver1", BuildFailedAt{Step: 1, ExitCode: 1}), Failed{Turn: "t1", TurnSeq: 1, Attempt: 1, Stage: StageVerify, Build: "ver1", Step: 1, Reason: ExitCode{Code: 1}, At: epoch}, ""},
		{"verify did not start", ver, fin("ver1", BuildStartFailedAt{Step: 0, Reason: "x"}), Failed{Turn: "t1", TurnSeq: 1, Attempt: 1, Stage: StageVerify, Build: "ver1", Step: 0, Reason: StartFailed{Failure: "x"}, At: epoch}, ""},
		{"verify cancelled", ver, fin("ver1", BuildCancelled{}), Cancelled{Turn: "t1", TurnSeq: 1, Attempt: 1, Cause: CancelExternal, At: epoch}, ""},
		{"verify interrupted", ver, fin("ver1", BuildInterrupted{}), Interrupted{Turn: "t1", TurnSeq: 1, Attempt: 1, Stage: StageVerify, At: epoch}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, cmds := Step(agg(tc.from), tc.in)
			if !reflect.DeepEqual(got.State, tc.want) || got.BootHash != tc.hash {
				t.Fatalf("got %#v (hash %q), want %#v (hash %q)", got.State, got.BootHash, tc.want, tc.hash)
			}
			wantEnq := 0
			if _, hop := tc.want.(Verifying); hop {
				wantEnq = 1
			}
			if count[EnqueueBuild](cmds) != wantEnq || count[WriteAudit](cmds) != 1 || count[Publish](cmds) != 1 {
				t.Fatalf("commands = %#v", cmds)
			}
		})
	}
}

func TestBuildFinishedIgnored(t *testing.T) {
	ver := Verifying{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "ver1", Plan: planBoot, Since: epoch}
	for name, in := range map[string]BuildFinished{
		"other build":       {Build: "zzz", Result: BuildPassed{}},
		"step out of range": {Build: "ver1", Result: BuildFailedAt{Step: 2, ExitCode: 1}},
		"negative step":     {Build: "ver1", Result: BuildFailedAt{Step: -1, ExitCode: 1}},
		"exit code zero":    {Build: "ver1", Result: BuildFailedAt{Step: 0, ExitCode: 0}},
		"empty start text":  {Build: "ver1", Result: BuildStartFailedAt{Step: 0}},
		"nil result":        {Build: "ver1"},
	} {
		if got, cmds := Step(agg(ver), in); changed(got, agg(ver)) || len(cmds) != 0 {
			t.Errorf("%s: had an effect", name)
		}
	}
	// A second delivery after a terminal state.
	done, _ := Step(agg(ver), BuildFinished{Build: "ver1", Result: BuildPassed{}, Now: epoch})
	if got, cmds := Step(done, BuildFinished{Build: "ver1", Result: BuildFailedAt{Step: 0, ExitCode: 1}}); changed(got, done) || len(cmds) != 0 {
		t.Error("a second BuildFinished moved a terminal state")
	}
}

func TestCancelAndEnqueueFailed(t *testing.T) {
	boot := Bootstrapping{Turn: "t1", TurnSeq: 1, Attempt: 1, Build: "boot1", Plan: planBoot, NextBuild: "ver1", DepsHash: "h1", Since: epoch}
	got, cmds := Step(agg(boot), Cancel{Now: epoch})
	if !reflect.DeepEqual(got.State, Cancelled{Turn: "t1", TurnSeq: 1, Attempt: 1, Cause: CancelUser, At: epoch}) || cmds[0] != (CancelBuild{Build: "boot1"}) {
		t.Fatalf("Cancel: %#v %#v", got.State, cmds)
	}
	if got, cmds := Step(got, Cancel{Now: epoch}); changed(got, got) || len(cmds) != 0 {
		t.Error("Cancel on a finished state had an effect")
	}
	got, cmds = Step(agg(boot), EnqueueFailed{Build: "boot1", Reason: "x", Now: epoch})
	if !reflect.DeepEqual(got.State, Unverifiable{Turn: "t1", TurnSeq: 1, Attempt: 1, Reason: UnverifiableCouldNotQueue}) || len(cmds) != 2 {
		t.Fatalf("EnqueueFailed: %#v %#v", got.State, cmds)
	}
	if got, cmds := Step(agg(boot), EnqueueFailed{Build: "other"}); changed(got, agg(boot)) || len(cmds) != 0 {
		t.Error("EnqueueFailed for another build had an effect")
	}
}

func TestSummarizeWorstOf(t *testing.T) {
	order := []State{
		Failed{}, Interrupted{}, Unverifiable{}, Cancelled{}, Verifying{}, Passed{}, Skipped{}, None{},
	}
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			if got, want := Summarize([]State{order[j], order[i]}), SummaryOf(order[i]); got != want {
				t.Errorf("%T beats %T: got %s, want %s", order[i], order[j], got, want)
			}
		}
	}
	if Summarize(nil) != SummaryNone || Summarize([]State{nil}) != SummaryNone {
		t.Error("empty summary is not none")
	}
	if SummaryOf(Bootstrapping{}) != SummaryOf(Verifying{}) {
		t.Error("bootstrapping and verifying must share a summary")
	}
}

func TestStepNilStateAndInput(t *testing.T) {
	v := newAggregate()
	v.State = nil
	if got, cmds := Step(v, Cancel{}); got.State != nil || cmds != nil {
		t.Error("nil state moved")
	}
	if got, cmds := Step(newAggregate(), nil); changed(got, newAggregate()) || cmds != nil {
		t.Error("nil input moved")
	}
}
