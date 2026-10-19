// Package toolchains lists the built-in toolchain modules (BL-10).
package toolchains

import (
	"io/fs"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/gradle"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/maven"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/node"
)

// All returns the detectable modules. The custom module is not here: it needs
// the repo's settings and never detects on its own.
func All() []builds.Toolchain {
	return []builds.Toolchain{node.New(), maven.New(), gradle.New()}
}

// Match is one toolchain that detected the repo.
type Match struct {
	Toolchain builds.Toolchain
	Detection builds.Detection
}

// DetectAll returns every module that matches repo, in All order.
func DetectAll(repo fs.FS) []Match {
	var out []Match
	for _, t := range All() {
		if d, ok := t.Detect(repo); ok {
			out = append(out, Match{Toolchain: t, Detection: d})
		}
	}
	return out
}
