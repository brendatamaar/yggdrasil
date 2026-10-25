package guard

// Decide is the path and command decision for one tool call (rows D1 to D15
// of docs/design/guard.md). It is stateless and pure.
//
// STUB (task G1 implements it): fails closed, every call is denied.
func Decide(p Policy, c Call) Verdict {
	_, _ = p, c
	return Deny(ReasonUnknownTool)
}
