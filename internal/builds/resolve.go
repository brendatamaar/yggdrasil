package builds

// ResolveCommands applies a repo's settings to the toolchain defaults (BL-08,
// WE-05). It is pure and the result shares no memory with its inputs:
//
//   - a non-empty VerifyOverride replaces Verify;
//   - FullTests appends the toolchain's FullTests steps after Verify (a
//     toolchain whose chain already runs the tests has none);
//   - Bootstrap == false empties Bootstrap.
//
// The detection is accepted so callers pass the pair together and a rule may
// depend on it later.
func ResolveCommands(_ Detection, cmds Commands, s RepoBuildSettings) Commands {
	out := Commands{
		Install:   cloneSteps(cmds.Install),
		Bootstrap: cloneSteps(cmds.Bootstrap),
		Verify:    cloneSteps(cmds.Verify),
		FullTests: cloneSteps(cmds.FullTests),
		Build:     cloneSteps(cmds.Build),
		DevServer: cloneSteps(cmds.DevServer),
	}
	if len(s.VerifyOverride) > 0 {
		out.Verify = nil
		for _, c := range s.VerifyOverride {
			out.Verify = append(out.Verify, Step{Name: c.Name, Argv: append([]string(nil), c.Argv...)})
		}
	}
	if s.FullTests {
		out.Verify = append(out.Verify, cloneSteps(cmds.FullTests)...)
	}
	if !s.Bootstrap {
		out.Bootstrap = nil
	}
	return out
}

func cloneSteps(in []Step) []Step {
	if len(in) == 0 {
		return nil
	}
	out := make([]Step, len(in))
	for i, s := range in {
		out[i] = Step{Name: s.Name, Argv: append([]string(nil), s.Argv...), Env: append([]string(nil), s.Env...)}
	}
	return out
}
