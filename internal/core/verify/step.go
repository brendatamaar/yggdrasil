package verify

// Step is (verification, input) -> (verification, commands): the transition
// table of docs/design/verify.md. An input it does not handle in the current
// state changes nothing and returns no commands.
//
// TODO(V1): decision logic. Foundations ship the signature only.
func Step(v Verification, in Input) (Verification, []Command) {
	_ = in
	return v, nil
}
