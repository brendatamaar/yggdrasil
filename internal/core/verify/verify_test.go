package verify

import "testing"

func TestEnumsValidate(t *testing.T) {
	for _, ok := range []interface{ Validate() error }{
		SkipNoChanges, SkipDisabled, UnverifiableNoCommand, UnverifiableNotTrusted,
		UnverifiableWorktreeMissing, UnverifiableCouldNotQueue, StageBootstrap, StageVerify,
		CancelUser, CancelExternal, Attempt(1), TurnID("t1"), StartFailure("npm was not found"), ExitCode{Code: 1},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%#v: %v", ok, err)
		}
	}
	for _, bad := range []interface{ Validate() error }{
		SkipReason("x"), UnverifiableReason(""), Stage("x"), CancelCause("x"), Attempt(0),
		TurnID("a b"), StartFailure(""), StartFailure("a\nb"), ExitCode{Code: 0},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%#v accepted", bad)
		}
	}
}

func TestVerificationValidate(t *testing.T) {
	v := Verification{ID: "v1", Task: "t", Repo: "r", Worktree: "w", State: None{}}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	v.State = Verifying{Turn: "t1", Attempt: 1, Build: "b1"}
	if v.Validate() == nil {
		t.Error("a Verifying state with an empty chain was accepted (V9)")
	}
	v.State = nil
	if v.Validate() == nil {
		t.Error("nil state accepted")
	}
}

func TestStepStubChangesNothing(t *testing.T) {
	v := Verification{ID: "v1", Task: "t", Repo: "r", Worktree: "w", State: None{}}
	got, cmds := Step(v, Cancel{})
	if got.State != v.State || len(cmds) != 0 {
		t.Fatal("stub changed state or emitted commands")
	}
}
