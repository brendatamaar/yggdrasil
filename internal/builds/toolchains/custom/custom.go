// Package custom is the fallback toolchain for ecosystems without a module
// (BL-15). Commands are argv lists from the repo's settings; there is no
// shell. It never matches a repo on its own.
package custom

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Command is one user-defined step.
type Command struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

// Config is the parsed custom-commands setting of a repo.
type Config struct {
	Install   []Command `json:"install,omitempty"`
	Verify    []Command `json:"verify,omitempty"`
	FullTests []Command `json:"fullTests,omitempty"`
	Build     []Command `json:"build,omitempty"`
	DevServer []Command `json:"devServer,omitempty"`
	Artifacts []string  `json:"artifacts,omitempty"`
}

// Toolchain implements builds.Toolchain from a validated Config.
type Toolchain struct{ cfg Config }

// New validates cfg and returns the toolchain. Every command needs a name and
// a non-empty argv, and no argument may hold a shell metacharacter.
func New(cfg Config) (Toolchain, error) {
	groups := []struct {
		name string
		cmds []Command
	}{{"install", cfg.Install}, {"verify", cfg.Verify}, {"full tests", cfg.FullTests},
		{"build", cfg.Build}, {"dev server", cfg.DevServer}}
	for _, g := range groups {
		for i, c := range g.cmds {
			if c.Name == "" {
				return Toolchain{}, fmt.Errorf("%s command %d has no name", g.name, i+1)
			}
			if len(c.Argv) == 0 || c.Argv[0] == "" {
				return Toolchain{}, fmt.Errorf("%s command %q is empty. Give the program and its arguments as a list", g.name, c.Name)
			}
			if err := platform.BatchArgsOK(c.Argv); err != nil {
				return Toolchain{}, fmt.Errorf("%s command %q: %w", g.name, c.Name, err)
			}
		}
	}
	for _, a := range cfg.Artifacts {
		if a == "" {
			return Toolchain{}, errors.New("an artifact pattern is empty")
		}
	}
	return Toolchain{cfg: cfg}, nil
}

// Name implements builds.Toolchain.
func (Toolchain) Name() string { return "custom" }

// Detect never matches: custom commands apply only when the user sets them.
func (Toolchain) Detect(fs.FS) (builds.Detection, bool) { return builds.Detection{}, false }

// Detection is what callers pass to Commands once the repo has custom settings.
func (Toolchain) Detection() builds.Detection { return builds.Detection{Toolchain: "custom", Dir: "."} }

func steps(cs []Command) []builds.Step {
	var out []builds.Step
	for _, c := range cs {
		out = append(out, builds.Step{Name: c.Name, Argv: append([]string(nil), c.Argv...)})
	}
	return out
}

// Commands implements builds.Toolchain.
func (t Toolchain) Commands(builds.Detection) builds.Commands {
	return builds.Commands{Install: steps(t.cfg.Install), Verify: steps(t.cfg.Verify),
		FullTests: steps(t.cfg.FullTests), Build: steps(t.cfg.Build), DevServer: steps(t.cfg.DevServer)}
}

// Artifacts implements builds.Toolchain.
func (t Toolchain) Artifacts(builds.Detection) []string {
	return append([]string(nil), t.cfg.Artifacts...)
}
