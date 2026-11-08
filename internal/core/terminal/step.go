package terminal

// Step is the terminal state machine: (registry2, commands) = Step(registry1,
// input). An input that is illegal for the state returns the registry
// unchanged and no commands (docs/design/terminal.md). Foundation stub: the
// transition table is implemented by the terminal task (P).
func Step(r Registry, in Input) (Registry, []Command) {
	return r, nil
}

// Run feeds inputs to Step in order and returns the final registry and every
// command emitted, in order.
func Run(r Registry, inputs []Input) (Registry, []Command) {
	var all []Command
	for _, in := range inputs {
		var cmds []Command
		r, cmds = Step(r, in)
		all = append(all, cmds...)
	}
	return r, all
}
