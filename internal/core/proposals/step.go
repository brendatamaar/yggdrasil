package proposals

// Step is (proposal, input) -> (proposal, commands): the lifecycle table of
// docs/design/proposals.md. Created applies to a proposal with a nil State.
// An input that is not handled in the current state, including Confirm or
// Reject on a decided proposal and any input with a stale Attempt, changes
// nothing.
//
// STUB (task X1 implements it): returns the proposal unchanged and no
// commands, so nothing can be confirmed or executed.
func Step(p Proposal, in Input) (Proposal, []Command) {
	_ = in
	return p, nil
}
