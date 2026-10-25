package guard

import (
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func genViews(r *rand.Rand) []TrustView {
	var vs []TrustView
	for n := r.Intn(4); n > 0; n-- {
		vs = append(vs, pick(r, TrustNone, TrustTrusted, TrustTrusted, TrustUntrusted, "bogus"))
	}
	return vs
}

// allTrusted is the reference for "every repo the mode touches is fine".
func allTrusted(vs []TrustView) bool {
	if len(vs) == 0 {
		return false
	}
	for _, v := range vs {
		if v != TrustNone && v != TrustTrusted {
			return false
		}
	}
	return true
}

// M5: one view per repo the mode touches; all must be fine.
func TestAdmitEveryRepoViewCounts(t *testing.T) {
	hook := EnforcementHook
	proj := func(vs ...TrustView) Request {
		return Request{Mode: core.ModeProject, OptIn: true, Trust: vs, Enforcement: hook}
	}
	code := func(vs ...TrustView) Request { return Request{Mode: core.ModeCode, Trust: vs, Enforcement: hook} }
	for name, c := range map[string]struct {
		req  Request
		want bool
	}{
		"all trusted":                      {proj(TrustTrusted, TrustTrusted, TrustNone), true},
		"one untrusted among trusted":      {proj(TrustTrusted, TrustUntrusted, TrustTrusted), false},
		"untrusted last":                   {proj(TrustNone, TrustTrusted, TrustUntrusted), false},
		"one unknown value":                {proj(TrustTrusted, "weird"), false},
		"empty list":                       {proj(), false},
		"nil list code":                    {Request{Mode: core.ModeCode, Enforcement: hook}, false},
		"single trusted code":              {code(TrustTrusted), true},
		"ask with untrusted":               {Request{Mode: core.ModeAsk, Trust: []TrustView{TrustUntrusted, TrustUntrusted}, Enforcement: hook}, true},
		"ask with empty list":              {Request{Mode: core.ModeAsk, Enforcement: hook}, true},
		"current branch one untrusted":     {Request{Mode: core.ModeCurrentBranch, OptIn: true, Trust: []TrustView{TrustTrusted, TrustUntrusted}, Enforcement: hook}, false},
		"code one untrusted among trusted": {code(TrustTrusted, TrustTrusted, TrustUntrusted), false},
	} {
		t.Run(name, func(t *testing.T) {
			a := Admit(c.req)
			if a.Admitted != c.want {
				t.Fatalf("got %+v, want admitted=%v", a, c.want)
			}
			if !c.want && a.Refusal != ReasonRepoUntrusted {
				t.Fatalf("refusal %q, want %q", a.Refusal, ReasonRepoUntrusted)
			}
		})
	}
}

// A1b: a non-Ask mode is admitted only when every view is None or Trusted.
func TestPropNonAskNeedsEveryViewTrusted(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			req := genRequest(r)
			if a := Admit(req); a.Admitted && req.Mode != core.ModeAsk && !allTrusted(req.Trust) {
				t.Fatalf("admitted %+v", req)
			}
		}
	})
}
