// Package golang is the Go toolchain (BL-12).
package golang

import (
	"bufio"
	"bytes"
	"io/fs"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/internal/find"
)

const maxSource = 1 << 20

// Toolchain implements builds.Toolchain for go.mod repos.
type Toolchain struct{}

// New returns the Go toolchain.
func New() Toolchain { return Toolchain{} }

// Name implements builds.Toolchain.
func (Toolchain) Name() string { return "golang" }

// Detect looks for go.mod. Facts["main"] is "true" when a non-test .go file
// in the module folder declares package main, so "go run ." has something to
// run. It reads files only.
func (Toolchain) Detect(repo fs.FS) (builds.Detection, bool) {
	dir, _, ok := find.Dir(repo, "go.mod")
	if !ok {
		return builds.Detection{}, false
	}
	facts := map[string]string{}
	if hasMain(repo, dir) {
		facts["main"] = "true"
	}
	return builds.Detection{Toolchain: "golang", Dir: dir, Runner: "go", Facts: facts}, true
}

func hasMain(repo fs.FS, dir string) bool {
	entries, err := fs.ReadDir(repo, dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := fs.ReadFile(repo, find.Join(dir, n))
		if err != nil || len(b) > maxSource {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 2 && f[0] == "package" {
				if f[1] == "main" {
					return true
				}
				break
			}
		}
	}
	return false
}

// Commands implements builds.Toolchain. Verify is vet then build; the tests
// are the opt-in full run (docs/PRD.md section 6.3).
func (Toolchain) Commands(d builds.Detection) builds.Commands {
	download := builds.Step{Name: "Download modules", Argv: []string{"go", "mod", "download"}}
	c := builds.Commands{
		Install:   []builds.Step{download},
		Bootstrap: []builds.Step{download},
		Verify: []builds.Step{
			{Name: "vet", Argv: []string{"go", "vet", "./..."}},
			{Name: "build", Argv: []string{"go", "build", "./..."}},
		},
		FullTests: []builds.Step{{Name: "test", Argv: []string{"go", "test", "./..."}}},
		Build:     []builds.Step{{Name: "build", Argv: []string{"go", "build", "./..."}}},
	}
	if d.Facts["main"] == "true" {
		c.DevServer = []builds.Step{{Name: "run", Argv: []string{"go", "run", "."}}}
	}
	return c
}

// Artifacts implements builds.Toolchain: the conventional bin folder.
func (Toolchain) Artifacts(d builds.Detection) []string {
	return []string{find.Join(d.Dir, "bin/**")}
}

// DepsFiles implements builds.DepsFiler.
func (Toolchain) DepsFiles(d builds.Detection) []string {
	return []string{find.Join(d.Dir, "go.mod"), find.Join(d.Dir, "go.sum")}
}
