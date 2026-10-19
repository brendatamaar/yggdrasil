// Package node is the Node.js toolchain (BL-02).
package node

import (
	"encoding/json"
	"io/fs"
	"sort"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/internal/find"
)

const maxManifest = 4 << 20

// Toolchain implements builds.Toolchain for package.json repos.
type Toolchain struct{}

// New returns the Node toolchain.
func New() Toolchain { return Toolchain{} }

// Name implements builds.Toolchain.
func (Toolchain) Name() string { return "node" }

// Env is the environment the Verify chain wants on top of the clean one.
// builds.Step has no env field, so the caller applies it.
func Env() []string { return []string{"CI=true"} }

// Detect reads package.json and the lock files. It runs nothing.
func (Toolchain) Detect(repo fs.FS) (builds.Detection, bool) {
	dir, file, ok := find.Dir(repo, "package.json")
	if !ok {
		return builds.Detection{}, false
	}
	data, err := fs.ReadFile(repo, find.Join(dir, file))
	if err != nil || len(data) > maxManifest {
		return builds.Detection{}, false
	}
	var pkg struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return builds.Detection{}, false
	}
	pm, locked := "npm", false
	switch {
	case find.Exists(repo, find.Join(dir, "pnpm-lock.yaml")):
		pm, locked = "pnpm", true
	case find.Exists(repo, find.Join(dir, "yarn.lock")):
		pm, locked = "yarn", true
	case find.Exists(repo, find.Join(dir, "package-lock.json")):
		locked = true
	}
	names := make([]string, 0, len(pkg.Scripts))
	for n := range pkg.Scripts {
		names = append(names, n)
	}
	sort.Strings(names)
	facts := map[string]string{"scripts": strings.Join(names, ",")}
	if locked {
		facts["locked"] = "true"
	}
	return builds.Detection{Toolchain: "node", Dir: dir, Runner: pm, Facts: facts}, true
}

func has(d builds.Detection, script string) bool {
	for _, s := range strings.Split(d.Facts["scripts"], ",") {
		if s == script {
			return true
		}
	}
	return false
}

func run(d builds.Detection, script string) builds.Step {
	return builds.Step{Name: script, Argv: []string{d.Runner, "run", script}}
}

// Commands implements builds.Toolchain. Verify is typecheck, lint, test (each
// only if the script exists), else build.
func (Toolchain) Commands(d builds.Detection) builds.Commands {
	var c builds.Commands
	switch {
	case d.Runner == "npm" && d.Facts["locked"] == "true":
		c.Install = []builds.Step{{Name: "install", Argv: []string{"npm", "ci"}}}
	case d.Runner == "npm":
		c.Install = []builds.Step{{Name: "install", Argv: []string{"npm", "install"}}}
	case d.Runner == "pnpm" || d.Runner == "yarn":
		c.Install = []builds.Step{{Name: "install", Argv: []string{d.Runner, "install", "--frozen-lockfile"}}}
	}
	for _, s := range []string{"typecheck", "lint", "test"} {
		if has(d, s) {
			c.Verify = append(c.Verify, run(d, s))
		}
	}
	if len(c.Verify) == 0 && has(d, "build") {
		c.Verify = []builds.Step{run(d, "build")}
	}
	if has(d, "build") {
		c.Build = []builds.Step{run(d, "build")}
	}
	switch {
	case has(d, "dev"):
		c.DevServer = []builds.Step{run(d, "dev")}
	case has(d, "start"):
		c.DevServer = []builds.Step{run(d, "start")}
	}
	return c
}

// EnvBuilds returns the per-environment build scripts ("build:staging") as
// named steps. They are alternatives to Commands.Build, so they are not in it.
func EnvBuilds(d builds.Detection) []builds.Step {
	var out []builds.Step
	for _, s := range strings.Split(d.Facts["scripts"], ",") {
		if strings.HasPrefix(s, "build:") && len(s) > len("build:") {
			out = append(out, run(d, s))
		}
	}
	return out
}

// Artifacts implements builds.Toolchain.
func (Toolchain) Artifacts(d builds.Detection) []string {
	return []string{find.Join(d.Dir, "dist/**"), find.Join(d.Dir, "build/**"), find.Join(d.Dir, "out/**")}
}
