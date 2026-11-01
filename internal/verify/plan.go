package verify

import (
	"io/fs"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// Notes shown to the user when a repo's own configuration stops verification.
const (
	NoteNoToolchain  = "No build tool was detected in this repo, so there is no verify command."
	NoteTooManySteps = "The verify or setup chain for this repo has too many steps. Shorten it in the repo's build settings."
	NoteInvalidStep  = "A verify or setup step for this repo is not valid. Check the repo's build settings."
	NoteBadSettings  = "The verify command for this repo is not valid. Fix it in the repo's build settings."
)

func toSpecs(in []builds.Step) []cv.StepSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]cv.StepSpec, len(in))
	for i, s := range in {
		out[i] = cv.StepSpec{Name: s.Name, Argv: append([]string(nil), s.Argv...)}
		if len(s.Env) > 0 {
			out[i].Env = append([]string(nil), s.Env...)
		}
	}
	return out
}

// ResolvePlan turns the repo's toolchain defaults and settings into a Plan
// (BL-08, WE-05). repo is rooted at the worktree folder. It reads files only.
// The first toolchain that detects the repo and has a verify chain wins.
//
// A resolved chain the core cannot run (more than cv.MaxSteps steps, or a step
// that does not validate) is not passed on: the plan then has no verify chain,
// so the verification ends Unverifiable{no_command}, and note says why in plain
// language. A FullTests append can exceed the limit, which is why this check
// lives here and not in the core, which would drop the whole input.
func ResolvePlan(repo fs.FS, toolchains []builds.Toolchain, s builds.RepoBuildSettings) (plan cv.Plan, note string) {
	for _, tc := range toolchains {
		det, ok := tc.Detect(repo)
		if !ok {
			continue
		}
		cmds := builds.ResolveCommands(det, tc.Commands(det), s)
		if len(cmds.Verify) == 0 {
			continue
		}
		p := cv.Plan{
			Bootstrap: toSpecs(cmds.Bootstrap),
			Verify:    toSpecs(cmds.Verify),
			DepsHash:  builds.HashDeps(repo, builds.DepsFiles(tc, det)),
		}
		if len(p.Bootstrap) > cv.MaxSteps || len(p.Verify) > cv.MaxSteps {
			return cv.Plan{}, NoteTooManySteps
		}
		if err := p.Validate(); err != nil || !queueable(p.Bootstrap) || !queueable(p.Verify) {
			return cv.Plan{}, NoteInvalidStep
		}
		return p, ""
	}
	return cv.Plan{}, NoteNoToolchain
}

// queueable reports whether the builds service would accept the chain (it
// also checks environment entries, which the core's own check does not).
func queueable(steps []cv.StepSpec) bool {
	if len(steps) == 0 {
		return true
	}
	spec, err := buildSpec("wt_check", "rep_check", steps)
	return err == nil && spec.Validate() == nil
}
