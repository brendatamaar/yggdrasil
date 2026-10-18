package repos

// Decide is the tracking step: (workspace2, commands, outcome) =
// Decide(workspace1, input). An input that is illegal for the workspace
// returns it unchanged, no commands and the outcome that says why.
func Decide(ws Workspace, in Input) (Workspace, []Command, Outcome) {
	// Implemented in batch B1 (docs/phases/phase-2.md).
	return ws, nil, NotFound{}
}

// Run feeds inputs to Decide in order and returns the final workspace, every
// command in order and every outcome in order. The property tests drive
// Decide through it.
func Run(ws Workspace, inputs []Input) (Workspace, []Command, []Outcome) {
	var cmds []Command
	var outs []Outcome
	for _, in := range inputs {
		var c []Command
		var o Outcome
		ws, c, o = Decide(ws, in)
		cmds = append(cmds, c...)
		outs = append(outs, o)
	}
	return ws, cmds, outs
}
