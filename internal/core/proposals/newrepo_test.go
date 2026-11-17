package proposals

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// everyView enumerates the whole (small) space of operation views, so the
// properties below hold for all of them, not for a sample.
func everyView() []OperationView {
	var out []OperationView
	phases := []OperationPhase{OpIdle, OpReady, OpRunning, OpUncertain, OpStopped, OpPublished}
	flows := []OperationFlow{FlowNone, FlowInit, FlowPublish}
	reasons := []string{"", "rejected", "untracked", "conflict", "not_implemented", "path_exists", "missing_author",
		"host_refused", "name_collision", "origin_exists", "unsupported_transport", "preconditions_changed", "stage_failed", "other"}
	for _, p := range phases {
		for _, f := range flows {
			for _, r := range reasons {
				out = append(out, OperationView{Phase: p, Flow: f, StopReason: r, Head: strings.Repeat("a", 40), URL: "https://github.com/acme/shop-new"})
			}
		}
	}
	return out
}

var newRepoKinds = []core.ProposalKind{core.ProposalInitRepo, core.ProposalPublish}

// A start is only planned when nothing ran; a retry only after a definite,
// retryable stop of the right flow (a confirm cannot resume a stopped
// operation, so the two are different plans). Neither is ever planned while a
// worker owns the operation, while a stage's outcome is unknown, or when the
// work is finished.
func TestPropPlanNewRepoNeverStartsWhileOwnedOrUnknown(t *testing.T) {
	for _, kind := range newRepoKinds {
		for _, v := range everyView() {
			plan := PlanNewRepo(kind, v)
			if plan == PlanNewRepoStart || plan == PlanNewRepoRetry {
				switch v.Phase {
				case OpRunning, OpUncertain, OpPublished:
					t.Fatalf("%s %+v: planned %s", kind, v, plan)
				}
			}
			if plan == PlanNewRepoStart {
				if v.Phase == OpStopped {
					t.Fatalf("%s %+v: a stopped operation is retried, never started again", kind, v)
				}
				if kind == core.ProposalInitRepo && v.Phase == OpReady {
					t.Fatalf("%s %+v: re-init of a ready repo", kind, v)
				}
				if kind == core.ProposalPublish && v.Phase == OpIdle {
					t.Fatalf("%s %+v: publish before the repo is ready", kind, v)
				}
			}
			if plan == PlanNewRepoRetry {
				if v.Phase != OpStopped || !StopRetryable(v.StopReason) {
					t.Fatalf("%s %+v: retry of an operation that is not stopped or never resumes", kind, v)
				}
				want := FlowInit
				if kind == core.ProposalPublish {
					want = FlowPublish
				}
				if v.Flow != want {
					t.Fatalf("%s %+v: retry in the wrong flow", kind, v)
				}
			}
			if v.Phase == OpUncertain && plan != PlanNewRepoReconcile && plan != PlanNewRepoRefuse && plan != PlanNewRepoDone {
				t.Fatalf("%s %+v: uncertain operation got plan %s", kind, v, plan)
			}
		}
	}
	if PlanNewRepo(core.ProposalCommit, OperationView{Phase: OpIdle}) != PlanNewRepoRefuse {
		t.Error("another kind was planned")
	}
}

// The plan table of the stopped phase, spelled out.
func TestPlanNewRepoStoppedRetriesOnlyResumableStops(t *testing.T) {
	for _, r := range []string{"path_exists", "missing_author", "stage_failed", "host_refused", "name_collision", "origin_exists", "unsupported_transport", "preconditions_changed"} {
		if got := PlanNewRepo(core.ProposalInitRepo, OperationView{Phase: OpStopped, Flow: FlowInit, StopReason: r}); got != PlanNewRepoRetry {
			t.Errorf("init stopped %s: plan %s, want retry", r, got)
		}
		if got := PlanNewRepo(core.ProposalPublish, OperationView{Phase: OpStopped, Flow: FlowPublish, StopReason: r}); got != PlanNewRepoRetry {
			t.Errorf("publish stopped %s: plan %s, want retry", r, got)
		}
	}
	for _, r := range []string{"rejected", "untracked", "conflict", "not_implemented"} {
		if got := PlanNewRepo(core.ProposalInitRepo, OperationView{Phase: OpStopped, Flow: FlowInit, StopReason: r}); got != PlanNewRepoRefuse {
			t.Errorf("init stopped %s: plan %s, want refuse", r, got)
		}
	}
}

// Done is only reported for a finished operation; an uncertain one is never
// failed; a failure keeps a plain message and is only retryable when the stop
// can resume.
func TestPropJudgeOperationNeverInventsAnOutcome(t *testing.T) {
	for _, kind := range newRepoKinds {
		for _, v := range everyView() {
			j := JudgeOperation(kind, v)
			switch j.Kind {
			case VerdictDone:
				finished := v.Phase == OpPublished || (kind == core.ProposalInitRepo && (v.Phase == OpReady || v.Flow == FlowPublish))
				if !finished {
					t.Fatalf("%s %+v: done but not finished", kind, v)
				}
			case VerdictFailed:
				if v.Phase != OpStopped && (kind != core.ProposalPublish || v.Flow == FlowPublish) {
					t.Fatalf("%s %+v: failed without a stop", kind, v)
				}
				if j.Message == "" || j.FailKind.Validate() != nil {
					t.Fatalf("%s %+v: failure %+v", kind, v, j)
				}
				if j.Retryable && !StopRetryable(v.StopReason) {
					t.Fatalf("%s %+v: retryable after a stop that never resumes", kind, v)
				}
			case VerdictPending:
				if v.Phase != OpRunning {
					t.Fatalf("%s %+v: pending but nothing runs", kind, v)
				}
			}
			if v.Phase == OpUncertain && v.Flow == FlowInit && kind == core.ProposalInitRepo && j.Kind != VerdictUnknown {
				t.Fatalf("%s %+v: unknown outcome reported as %s", kind, v, j.Kind)
			}
			// Reconcile never claims more than the verdict proves.
			out, _ := ReconcileOf(j)
			if out == ReconcileDone && j.Kind != VerdictDone {
				t.Fatalf("%s %+v: reconcile done without a done verdict", kind, v)
			}
		}
	}
}

func TestJudgeOperationResults(t *testing.T) {
	head := strings.Repeat("c", 40)
	j := JudgeOperation(core.ProposalInitRepo, OperationView{Phase: OpReady, Head: head})
	if j.Kind != VerdictDone || j.Result.CommitID != head {
		t.Errorf("init ready: %+v", j)
	}
	j = JudgeOperation(core.ProposalPublish, OperationView{Phase: OpPublished, Head: head, URL: "https://github.com/acme/shop-new"})
	if j.Kind != VerdictDone || j.Result.CommitID != head || !strings.Contains(j.Result.Note, "acme/shop-new") {
		t.Errorf("published: %+v", j)
	}
	if j := JudgeOperation(core.ProposalPublish, OperationView{Phase: OpStopped, Flow: FlowPublish, StopReason: "name_collision"}); j.Kind != VerdictFailed || j.FailKind != FailRemote || !j.Retryable {
		t.Errorf("collision: %+v", j)
	}
	if j := JudgeOperation(core.ProposalInitRepo, OperationView{Phase: OpStopped, Flow: FlowInit, StopReason: "rejected"}); j.Retryable {
		t.Errorf("rejected is retryable: %+v", j)
	}
	if j := JudgeOperation(core.ProposalCommit, OperationView{Phase: OpReady}); j.Kind != VerdictUnknown {
		t.Errorf("other kind: %+v", j)
	}
}

func TestFreshAttemptIsAboveEverythingSeen(t *testing.T) {
	for n := -1; n < 5; n++ {
		for _, seen := range [][]int{nil, {0}, {1}, {3, 1}, {7, 2, 5}} {
			got := FreshAttempt(n, seen)
			if got < 1 || got < n {
				t.Fatalf("FreshAttempt(%d, %v) = %d", n, seen, got)
			}
			for _, s := range seen {
				if got <= s {
					t.Fatalf("FreshAttempt(%d, %v) = %d is not above %d", n, seen, got, s)
				}
			}
		}
	}
	if FreshAttempt(1, nil) != 1 {
		t.Error("a first attempt is not 1")
	}
}
