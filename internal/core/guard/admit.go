package guard

import "github.com/brendatamaar/yggdrasil/internal/core"

// Admit decides whether a turn may run in a mode (the Admit table of
// docs/design/guard.md). It is stateless and pure.
//
// STUB (task G1 implements it): fails closed, only Ask is admitted.
func Admit(r Request) Admission {
	if r.Mode == core.ModeAsk {
		return Admission{Admitted: true}
	}
	return Admission{Refusal: ReasonModeUnavailable}
}
