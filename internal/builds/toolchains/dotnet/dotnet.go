// Package dotnet is the .NET toolchain (BL-13).
package dotnet

import (
	"io/fs"
	"sort"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/internal/find"
)

const maxProject = 1 << 20

// Toolchain implements builds.Toolchain for .NET repos.
type Toolchain struct{}

// New returns the .NET toolchain.
func New() Toolchain { return Toolchain{} }

// Name implements builds.Toolchain.
func (Toolchain) Name() string { return "dotnet" }

// Detect looks for a solution (.sln or .slnx, preferred), else a .csproj,
// else global.json. The folder is the repo root, else the first sub folder.
// Facts["kind"] is "sln", "csproj" or "global"; Facts["target"] names the
// solution or project file (absent for global.json alone); Facts["web"] is
// "true" when the project uses the Web SDK. It reads files only.
func (Toolchain) Detect(repo fs.FS) (builds.Detection, bool) {
	for _, dir := range candidates(repo) {
		target, kind := pick(fileNames(repo, dir))
		if kind == "" {
			continue
		}
		facts := map[string]string{"kind": kind}
		if target != "" {
			facts["target"] = target
		}
		if kind == "csproj" && isWeb(repo, find.Join(dir, target)) {
			facts["web"] = "true"
		}
		return builds.Detection{Toolchain: "dotnet", Dir: dir, Runner: "dotnet", Facts: facts}, true
	}
	return builds.Detection{}, false
}

func candidates(repo fs.FS) []string {
	out := []string{"."}
	entries, err := fs.ReadDir(repo, ".")
	if err != nil {
		return out
	}
	var subs []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") {
			continue
		}
		switch n {
		case "node_modules", "target", "build", "dist", "vendor", "bin", "obj":
			continue
		}
		subs = append(subs, n)
	}
	sort.Strings(subs)
	return append(out, subs...)
}

func fileNames(repo fs.FS, dir string) []string {
	entries, err := fs.ReadDir(repo, dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func pick(names []string) (target, kind string) {
	var csproj, global string
	for _, n := range names {
		l := strings.ToLower(n)
		switch {
		case strings.HasSuffix(l, ".sln") || strings.HasSuffix(l, ".slnx"):
			return n, "sln"
		case strings.HasSuffix(l, ".csproj") && csproj == "":
			csproj = n
		case l == "global.json":
			global = n
		}
	}
	if csproj != "" {
		return csproj, "csproj"
	}
	if global != "" {
		return "", "global"
	}
	return "", ""
}

func isWeb(repo fs.FS, name string) bool {
	b, err := fs.ReadFile(repo, name)
	return err == nil && len(b) < maxProject && strings.Contains(string(b), "Microsoft.NET.Sdk.Web")
}

func argv(d builds.Detection, args ...string) []string {
	out := append([]string{"dotnet"}, args...)
	if t := d.Facts["target"]; t != "" {
		out = append(out, t)
	}
	return out
}

// Commands implements builds.Toolchain. Verify is a build without restore
// (Bootstrap restores first); tests are the opt-in full run because they
// often need services.
func (Toolchain) Commands(d builds.Detection) builds.Commands {
	restore := builds.Step{Name: "Restore packages", Argv: argv(d, "restore")}
	c := builds.Commands{
		Install:   []builds.Step{restore},
		Bootstrap: []builds.Step{restore},
		Verify:    []builds.Step{{Name: "build", Argv: append(argv(d, "build"), "--no-restore")}},
		FullTests: []builds.Step{{Name: "test", Argv: argv(d, "test")}},
		Build:     []builds.Step{{Name: "build", Argv: argv(d, "build")}},
	}
	// "dotnet run" needs one project; a solution has none to pick.
	if d.Facts["kind"] == "csproj" {
		s := builds.Step{Name: "run", Argv: []string{"dotnet", "run", "--project", d.Facts["target"]}}
		if d.Facts["web"] == "true" {
			s.Env = []string{"ASPNETCORE_URLS=http://127.0.0.1:{port}"}
		}
		c.DevServer = []builds.Step{s}
	}
	return c
}

// Artifacts implements builds.Toolchain.
func (Toolchain) Artifacts(d builds.Detection) []string {
	return []string{find.Join(d.Dir, "**/bin/Release/**"), find.Join(d.Dir, "publish/**")}
}

// DepsFiles implements builds.DepsFiler.
func (Toolchain) DepsFiles(d builds.Detection) []string {
	out := []string{find.Join(d.Dir, "global.json"), find.Join(d.Dir, "Directory.Packages.props"),
		find.Join(d.Dir, "packages.lock.json")}
	if t := d.Facts["target"]; t != "" {
		out = append(out, find.Join(d.Dir, t))
	}
	return out
}
