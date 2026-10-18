package builds

// Step is the build state machine: (state2, commands) = Step(state1, input).
// An input that is illegal for the state returns the state unchanged and no
// commands (docs/design/builds.md, transition table).
func Step(s State, in Input) (State, []Command) {
	// Implemented in batch B1 (docs/phases/phase-2.md).
	return s, nil
}

// Run feeds inputs to Step in order and returns the final state and every
// command emitted, in order. The property tests drive Step through it.
func Run(s State, inputs []Input) (State, []Command) {
	var all []Command
	for _, in := range inputs {
		var cmds []Command
		s, cmds = Step(s, in)
		all = append(all, cmds...)
	}
	return s, all
}
