package actions

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestModeAllowedOnlyForTheKindsThatSubmitATask(t *testing.T) {
	modes := []core.TaskMode{core.ModeAsk, core.ModeCode, "plan", ""}
	for _, k := range Kinds() {
		for _, m := range modes {
			want := (m == core.ModeAsk && k.Effect() == EffectSubmitTask) || (m == core.ModeCode && k == FixPipeline)
			if got := ModeAllowed(k, m); got != want {
				t.Errorf("ModeAllowed(%s, %q) = %v, want %v", k, m, got, want)
			}
		}
		// The mode a kind asks for is allowed exactly when it submits a task.
		if got, want := ModeAllowed(k, k.Mode()), k.Effect() == EffectSubmitTask; got != want {
			t.Errorf("kind %s: its own mode allowed = %v, want %v", k, got, want)
		}
	}
	if ModeAllowed(DiagnoseRepo, core.ModeCode) || ModeAllowed("nope", core.ModeAsk) {
		t.Error("Code is only for FixPipeline and unknown kinds are never allowed")
	}
}

func TestStillCurrent(t *testing.T) {
	cases := []struct {
		expected, now string
		want          bool
	}{
		{"a", "a", true}, {"a", "b", false}, {"", "", true}, {"", "a", false}, {"a", "", false},
	}
	for _, c := range cases {
		if got := StillCurrent(c.expected, c.now); got != c.want {
			t.Errorf("StillCurrent(%q, %q) = %v, want %v", c.expected, c.now, got, c.want)
		}
	}
}
