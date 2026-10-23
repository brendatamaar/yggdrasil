// Package maven is the Maven toolchain (BL-01) with dependency ordering
// (BL-04).
package maven

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/internal/find"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// maxReactor bounds how many reactor modules Modules reads.
const maxReactor = 200

// Toolchain implements builds.Toolchain and builds.Orderer.
type Toolchain struct{}

// New returns the Maven toolchain.
func New() Toolchain { return Toolchain{} }

// Name implements builds.Toolchain.
func (Toolchain) Name() string { return "maven" }

// Detect looks for pom.xml. A wrapper (mvnw) is preferred over mvn.
func (Toolchain) Detect(repo fs.FS) (builds.Detection, bool) {
	dir, _, ok := find.Dir(repo, "pom.xml")
	if !ok {
		return builds.Detection{}, false
	}
	runner := "mvn"
	if find.Exists(repo, find.Join(dir, "mvnw")) || find.Exists(repo, find.Join(dir, "mvnw.cmd")) {
		runner = "mvnw"
	}
	return builds.Detection{Toolchain: "maven", Dir: dir, Runner: runner}, true
}

func argv(d builds.Detection, args ...string) []string {
	r := d.Runner
	if r == "" {
		r = "mvn"
	}
	return append([]string{r}, args...)
}

// Commands implements builds.Toolchain.
func (Toolchain) Commands(d builds.Detection) builds.Commands {
	return builds.Commands{
		Verify:    []builds.Step{{Name: "test-compile", Argv: argv(d, "-B", "test-compile")}},
		FullTests: []builds.Step{{Name: "test", Argv: argv(d, "-B", "test")}},
		Build:     []builds.Step{{Name: "build", Argv: argv(d, "clean", "install", "-B")}},
	}
}

// Artifacts implements builds.Toolchain.
func (Toolchain) Artifacts(d builds.Detection) []string {
	return []string{find.Join(d.Dir, "target/*.jar"), find.Join(d.Dir, "target/*.war")}
}

// Modules implements builds.Orderer. It returns the root pom and the modules
// the root lists (reactor only, followed recursively, capped), reading files
// only.
func (Toolchain) Modules(repo fs.FS, d builds.Detection) ([]corebuilds.Module, error) {
	var out []corebuilds.Module
	seen := map[string]bool{}
	truncated := false
	var walk func(dir string) error
	walk = func(dir string) error {
		if seen[dir] {
			return nil
		}
		if len(seen) >= maxReactor {
			truncated = true
			return nil
		}
		seen[dir] = true
		name := find.Join(dir, "pom.xml")
		f, err := repo.Open(name)
		if err != nil {
			return fmt.Errorf("open %s: %w", name, err)
		}
		m, mods, err := parsePOM(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, m)
		for _, sub := range mods {
			sd := path.Join(dir, strings.ReplaceAll(sub, "\\", "/"))
			if !fs.ValidPath(sd) {
				continue // would leave the repo
			}
			if symlinkedPath(repo, sd) {
				return fmt.Errorf("the module folder %s is a symbolic link; Yggdrasil does not follow links inside a repo", sd)
			}
			if err := walk(sd); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(path.Clean(d.Dir)); err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("this reactor lists more than %d modules, so the build order was not worked out; order the repos by hand", maxReactor)
	}
	return out, nil
}

// symlinkedPath reports whether dir, or a folder above it, is a symbolic
// link. A missing entry is not one (the open that follows reports it).
func symlinkedPath(repo fs.FS, dir string) bool {
	parts := strings.Split(dir, "/")
	for i := range parts {
		fi, err := fs.Lstat(repo, strings.Join(parts[:i+1], "/"))
		if err != nil {
			return false
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return true
		}
	}
	return false
}
