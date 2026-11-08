package devservers

// Step is the dev-server state machine: (state2, commands) = Step(state1,
// input). An input that is illegal for the state returns the state unchanged
// and no commands (docs/design/devservers.md). Foundation stub: the
// transition table is implemented by the devservers task (D).
func Step(s Server, in Input) (Server, []Command) {
	return s, nil
}

// Run feeds inputs to Step in order and returns the final state and every
// command emitted, in order. The property tests drive Step through it.
func Run(s Server, inputs []Input) (Server, []Command) {
	var all []Command
	for _, in := range inputs {
		var cmds []Command
		s, cmds = Step(s, in)
		all = append(all, cmds...)
	}
	return s, all
}
