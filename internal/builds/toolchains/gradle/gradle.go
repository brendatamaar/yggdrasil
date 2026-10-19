// Package gradle is the Gradle toolchain (BL-11).
package gradle

import (
	"io/fs"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/internal/find"
)

// Toolchain implements builds.Toolchain.
type Toolchain struct{}

// New returns the Gradle toolchain.
func New() Toolchain { return Toolchain{} }

// Name implements builds.Toolchain.
func (Toolchain) Name() string { return "gradle" }

var buildFiles = []string{"build.gradle.kts", "build.gradle", "settings.gradle.kts", "settings.gradle"}

// Detect looks for Gradle build files; the wrapper is preferred. Kotlin is
// reported in Facts["lang"] when a build file mentions kotlin.
func (Toolchain) Detect(repo fs.FS) (builds.Detection, bool) {
	dir, _, ok := find.Dir(repo, buildFiles...)
	if !ok {
		return builds.Detection{}, false
	}
	runner := "gradle"
	if find.Exists(repo, find.Join(dir, "gradlew")) || find.Exists(repo, find.Join(dir, "gradlew.bat")) {
		runner = "gradlew"
	}
	lang := "java"
	for _, f := range buildFiles[:2] {
		if b, err := fs.ReadFile(repo, find.Join(dir, f)); err == nil && len(b) < 1<<20 &&
			strings.Contains(strings.ToLower(string(b)), "kotlin") {
			lang = "kotlin"
		}
	}
	return builds.Detection{Toolchain: "gradle", Dir: dir, Runner: runner, Facts: map[string]string{"lang": lang}}, true
}

// Commands implements builds.Toolchain.
func (Toolchain) Commands(d builds.Detection) builds.Commands {
	r := d.Runner
	if r == "" {
		r = "gradle"
	}
	tasks := []string{"compileJava", "compileTestJava"}
	if d.Facts["lang"] == "kotlin" {
		tasks = []string{"compileKotlin", "compileTestKotlin"}
	}
	return builds.Commands{
		Verify:    []builds.Step{{Name: "compile", Argv: append([]string{r}, tasks...)}},
		FullTests: []builds.Step{{Name: "test", Argv: []string{r, "test"}}},
		Build:     []builds.Step{{Name: "build", Argv: []string{r, "build"}}},
	}
}

// Artifacts implements builds.Toolchain.
func (Toolchain) Artifacts(d builds.Detection) []string {
	return []string{find.Join(d.Dir, "build/libs/*.jar")}
}
