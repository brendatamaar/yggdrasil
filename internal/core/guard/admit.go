package guard

import "github.com/brendatamaar/yggdrasil/internal/core"

// Admit decides whether a turn may run in a mode (the Admit table of
// docs/design/guard.md). It is stateless and pure. Unknown values refuse.
func Admit(r Request) Admission {
	if r.Mode.Validate() != nil {
		return refuse(ReasonModeUnavailable)
	}
	// An untrusted repo (or an unknown trust value) admits Ask only. Every
	// repo the mode touches must be trusted, and a list that names no repo
	// proves nothing, so it admits Ask only too.
	if r.Mode != core.ModeAsk && !allReposTrusted(r.Trust) {
		return refuse(ReasonRepoUntrusted)
	}
	// A switch goes between Ask and the task's own writing mode, nothing else.
	if r.Previous != nil {
		prev := *r.Previous
		writing := prev.Validate() == nil && prev != core.ModeAsk
		if r.Mode != core.ModeAsk && (!writing || r.Mode != prev) {
			return refuse(ReasonModeSwitchNotAllowed)
		}
	}
	switch r.Mode {
	case core.ModeAsk:
		return Admission{Admitted: true}
	case core.ModeCode:
		if r.Enforcement == EnforcementHook || r.Enforcement == EnforcementLimited {
			return Admission{Admitted: true}
		}
		return refuse(ReasonModeUnavailable)
	case core.ModeCurrentBranch:
		return wide(r, WarningEditsMainCheckout)
	case core.ModeProject:
		return wide(r, WarningEditsAllRepos)
	}
	return refuse(ReasonModeUnavailable)
}

func allReposTrusted(views []TrustView) bool {
	if len(views) == 0 {
		return false
	}
	for _, v := range views {
		if v != TrustNone && v != TrustTrusted {
			return false
		}
	}
	return true
}

// wide admits a mode that edits main checkouts: it needs the opt-in and a
// hook that enforces the limits.
func wide(r Request, w Warning) Admission {
	if !r.OptIn {
		return refuse(ReasonNeedsOptIn)
	}
	if r.Enforcement != EnforcementHook {
		return refuse(ReasonLimitedEnforcement)
	}
	return Admission{Admitted: true, Warning: w}
}

func refuse(r Reason) Admission { return Admission{Refusal: r} }
