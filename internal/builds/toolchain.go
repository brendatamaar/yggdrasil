package builds

import (
	"io/fs"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// Toolchain is one language ecosystem (BL-10, docs/AGENTS.md 5.7). Each lives
// in its own package under internal/builds/toolchains/<name>/ and implements
// this interface. The rules:
//
//   - Detect reads files only (lock files, manifests, pyproject.toml
//     sections). It never runs a command and never touches the network.
//   - A repo can match more than one toolchain; every match is offered.
//     Toolchain packages never import each other.
//   - Commands returns the defaults. The default Verify chain is the fast
//     chain of docs/PRD.md section 6.3: each step is present only if the repo
//     has it configured, the chain stops at the first failure (Step in
//     internal/core/builds does that), and it never contains a step that
//     needs a database or network services.
//   - Commands run through the project's own runner (mvnw, gradlew, uv run,
//     poetry run, the venv's Python), named by Argv[0] relative to the repo
//     (".venv/python"; "python-base" is the interpreter that creates the venv);
//     internal/platform resolves it (mvnw.cmd vs ./mvnw, npm.cmd, ...).
//     A toolchain never resolves executables or builds paths itself.
//   - Every repo can override any command in its build settings, and the
//     custom toolchain covers ecosystems without a module.
type Toolchain interface {
	// Name is the stable identifier of the toolchain ("node", "maven", ...).
	Name() string
	// Detect reports whether the repo uses this toolchain. repo is rooted at
	// the repo directory. The Detection is passed back to Commands, Artifacts
	// and Modules, so Detect reads each file once.
	Detect(repo fs.FS) (Detection, bool)
	// Commands returns the default commands for what Detect found.
	Commands(d Detection) Commands
	// Artifacts returns glob patterns, relative to the repo with forward
	// slashes, for the files a successful build produces (BL-07).
	Artifacts(d Detection) []string
}

// Orderer is implemented by toolchains that know how repos depend on each
// other, such as Maven (BL-04). It is optional: callers check for it with a
// type assertion.
type Orderer interface {
	// Modules reads the repo's module coordinates and dependencies from its
	// manifests, for corebuilds.OrderModules. The module's RepoID is left
	// empty; the caller sets it. It reads files only.
	Modules(repo fs.FS, d Detection) ([]corebuilds.Module, error)
}

// Detection is what a toolchain found in a repo.
type Detection struct {
	// Toolchain is the Name of the toolchain that produced it.
	Toolchain string
	// Dir is the folder, relative to the repo with forward slashes, that
	// holds the manifest ("." for the repo root). Commands start there.
	Dir string
	// Runner names the project's own runner when there is a choice ("pnpm",
	// "mvnw", "uv"). Empty when the toolchain has only one.
	Runner string
	// Facts holds toolchain-specific findings, such as the names of the
	// scripts a package.json declares. Keys and values are plain text.
	Facts map[string]string
}

// Step is one default command: a name for the UI and an argv list. It has no
// RepoID; Spec adds the repo.
type Step struct {
	Name string
	Argv []string
	// Env is extra environment for this step as KEY=VALUE entries (for
	// example "CI=true"), added to the clean environment. It never reaches a
	// step through os.Environ and token-like names are refused.
	Env []string
}

// Spec turns the step into a build step for repo.
func (s Step) Spec(repo core.RepoID) corebuilds.StepSpec {
	return corebuilds.StepSpec{
		RepoID: repo, Name: s.Name,
		Argv: append([]string(nil), s.Argv...), Env: append([]string(nil), s.Env...),
	}
}

// Commands are the defaults of one toolchain. A list is empty when the repo
// has nothing configured for that purpose.
type Commands struct {
	// Install installs dependencies in the main checkout. It changes the
	// repo, so running it is a proposal (docs/AGENTS.md section 6, invariant
	// 2), never automatic.
	Install []Step
	// Bootstrap prepares a task's own worktree before its first verification
	// (WE-05, docs/design/verify.md): restore dependencies so the verify chain
	// can run. It is automatic, but only inside a task worktree (never a main
	// checkout, so invariant 2 does not apply), only for a trusted repo
	// (SF-09), and through platform.Start with the clean environment.
	// Install stays a proposal; Bootstrap never replaces it.
	Bootstrap []Step
	// Verify is the fast chain (lint, typecheck, unit tests, ...).
	Verify []Step
	// FullTests is the slow test run, kept out of Verify.
	FullTests []Step
	// Build produces the artifacts.
	Build []Step
	// DevServer starts the dev server (usually one step).
	DevServer []Step
}
