package mcp

// Offered lists the tools a task is offered in a context (the Offered table of
// docs/design/mcp.md).
//
// STUB (task M1 implements it): fails closed, nothing is offered.
func Offered(c Context) []Tool {
	_ = c
	return nil
}

// Authorize decides whether a call of tool t is allowed in the context. It
// agrees with Offered for every (context, tool) (M1). Repo arguments are
// checked by the shell against the visible set and the scope with the same
// answer for every failure (M3).
//
// STUB (task M1 implements it): fails closed, every call is denied.
func Authorize(c Context, t Tool) Decision {
	_, _ = c, t
	return Decision{Reason: DenyUnknown}
}
