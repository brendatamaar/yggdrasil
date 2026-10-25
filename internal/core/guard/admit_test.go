package guard

import (
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func modePtr(m core.TaskMode) *core.TaskMode { return &m }

// TestAdmitRows covers every row of the Admit table.
func TestAdmitRows(t *testing.T) {
	hook, lim := EnforcementHook, EnforcementLimited
	cases := []struct {
		name string
		req  Request
		want Admission
	}{
		{"untrusted ask", Request{Mode: core.ModeAsk, Trust: TrustUntrusted, Enforcement: hook}, Admission{Admitted: true}},
		{"untrusted code", Request{Mode: core.ModeCode, Trust: TrustUntrusted, Enforcement: hook}, Admission{Refusal: ReasonRepoUntrusted}},
		{"untrusted current_branch with opt-in", Request{Mode: core.ModeCurrentBranch, OptIn: true, Trust: TrustUntrusted, Enforcement: hook}, Admission{Refusal: ReasonRepoUntrusted}},
		{"untrusted project with opt-in", Request{Mode: core.ModeProject, OptIn: true, Trust: TrustUntrusted, Enforcement: hook}, Admission{Refusal: ReasonRepoUntrusted}},
		{"unknown trust value is untrusted", Request{Mode: core.ModeCode, Trust: "weird", Enforcement: hook}, Admission{Refusal: ReasonRepoUntrusted}},
		{"empty trust value is untrusted", Request{Mode: core.ModeCode, Enforcement: hook}, Admission{Refusal: ReasonRepoUntrusted}},
		{"ask limited", Request{Mode: core.ModeAsk, Trust: TrustNone, Enforcement: lim}, Admission{Admitted: true}},
		{"code hook", Request{Mode: core.ModeCode, Trust: TrustTrusted, Enforcement: hook}, Admission{Admitted: true}},
		{"code limited", Request{Mode: core.ModeCode, Trust: TrustNone, Enforcement: lim}, Admission{Admitted: true}},
		{"code unknown enforcement", Request{Mode: core.ModeCode, Trust: TrustNone, Enforcement: "x"}, Admission{Refusal: ReasonModeUnavailable}},
		{"current_branch opt-in hook", Request{Mode: core.ModeCurrentBranch, OptIn: true, Trust: TrustNone, Enforcement: hook}, Admission{Admitted: true, Warning: WarningEditsMainCheckout}},
		{"current_branch no opt-in", Request{Mode: core.ModeCurrentBranch, Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonNeedsOptIn}},
		{"current_branch limited with opt-in", Request{Mode: core.ModeCurrentBranch, OptIn: true, Trust: TrustNone, Enforcement: lim}, Admission{Refusal: ReasonLimitedEnforcement}},
		{"current_branch no opt-in and limited", Request{Mode: core.ModeCurrentBranch, Trust: TrustNone, Enforcement: lim}, Admission{Refusal: ReasonNeedsOptIn}},
		{"project opt-in hook", Request{Mode: core.ModeProject, OptIn: true, Trust: TrustTrusted, Enforcement: hook}, Admission{Admitted: true, Warning: WarningEditsAllRepos}},
		{"project no opt-in", Request{Mode: core.ModeProject, Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonNeedsOptIn}},
		{"project limited", Request{Mode: core.ModeProject, OptIn: true, Trust: TrustNone, Enforcement: lim}, Admission{Refusal: ReasonLimitedEnforcement}},
		{"unknown mode", Request{Mode: "yolo", Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonModeUnavailable}},
		{"switch ask to ask", Request{Mode: core.ModeAsk, Previous: modePtr(core.ModeAsk), Trust: TrustNone, Enforcement: hook}, Admission{Admitted: true}},
		{"switch code to ask", Request{Mode: core.ModeAsk, Previous: modePtr(core.ModeCode), Trust: TrustNone, Enforcement: hook}, Admission{Admitted: true}},
		{"switch back to the write mode", Request{Mode: core.ModeCode, Previous: modePtr(core.ModeCode), Trust: TrustNone, Enforcement: hook}, Admission{Admitted: true}},
		{"switch code to current_branch", Request{Mode: core.ModeCurrentBranch, Previous: modePtr(core.ModeCode), OptIn: true, Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonModeSwitchNotAllowed}},
		{"switch code to project", Request{Mode: core.ModeProject, Previous: modePtr(core.ModeCode), OptIn: true, Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonModeSwitchNotAllowed}},
		{"switch ask-task to code", Request{Mode: core.ModeCode, Previous: modePtr(core.ModeAsk), Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonModeSwitchNotAllowed}},
		{"switch project to code", Request{Mode: core.ModeCode, Previous: modePtr(core.ModeProject), Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonModeSwitchNotAllowed}},
		{"switch to previous wide mode keeps opt-in rule", Request{Mode: core.ModeProject, Previous: modePtr(core.ModeProject), Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonNeedsOptIn}},
		{"switch with unknown previous", Request{Mode: core.ModeCode, Previous: modePtr("x"), Trust: TrustNone, Enforcement: hook}, Admission{Refusal: ReasonModeSwitchNotAllowed}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Admit(c.req); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func genRequest(r *rand.Rand) Request {
	req := Request{
		Mode:        pick(r, append(append([]core.TaskMode(nil), allModes...), "bogus")...),
		OptIn:       r.Intn(2) == 0,
		Trust:       pick(r, TrustNone, TrustTrusted, TrustUntrusted, "bogus"),
		Enforcement: pick(r, EnforcementHook, EnforcementLimited, "bogus"),
	}
	if r.Intn(2) == 0 {
		m := pick(r, append(append([]core.TaskMode(nil), allModes...), "bogus")...)
		req.Previous = &m
	}
	return req
}

// A1
func TestPropUntrustedOnlyAsk(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			req := genRequest(r)
			if req.Trust == TrustNone || req.Trust == TrustTrusted {
				continue
			}
			if a := Admit(req); a.Admitted && req.Mode != core.ModeAsk {
				t.Fatalf("untrusted repo admitted %+v: %+v", req, a)
			}
		}
	})
}

// A2
func TestPropWideModesNeedHookAndOptIn(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			req := genRequest(r)
			if req.Mode != core.ModeCurrentBranch && req.Mode != core.ModeProject {
				continue
			}
			if a := Admit(req); a.Admitted && (!req.OptIn || req.Enforcement != EnforcementHook || a.Warning == WarningNone) {
				t.Fatalf("wide mode admitted: %+v -> %+v", req, a)
			}
		}
	})
}

// A3
func TestPropSwitchStaysNarrow(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			req := genRequest(r)
			if req.Previous == nil {
				continue
			}
			if a := Admit(req); a.Admitted && req.Mode != core.ModeAsk && req.Mode != *req.Previous {
				t.Fatalf("switch admitted outside {ask, previous}: %+v", req)
			}
			if a := Admit(req); a.Admitted && req.Mode != core.ModeAsk && *req.Previous == core.ModeAsk {
				t.Fatalf("an Ask-only task switched to a writing mode: %+v", req)
			}
		}
	})
}

func TestAdmitRefusalsHaveMessages(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		a := Admit(genRequest(r))
		if a.Admitted && a.Refusal != "" {
			t.Fatalf("admitted with a refusal: %+v", a)
		}
		if !a.Admitted && (a.Refusal == "" || a.Warning != WarningNone || a.Refusal.Message() == "") {
			t.Fatalf("bad refusal: %+v", a)
		}
	})
}
