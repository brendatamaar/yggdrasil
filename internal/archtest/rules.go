package archtest

import (
	"fmt"
	"strings"
)

const module = "github.com/brendatamaar/yggdrasil/"

// A rule returns the violations one file commits.
type rule struct {
	name  string
	check func(f file) []string
}

func inDir(f file, dirs ...string) bool {
	for _, d := range dirs {
		if f.dir == d || strings.HasPrefix(f.dir, d+"/") {
			return true
		}
	}
	return false
}

func violation(f file, format string, a ...any) string {
	return fmt.Sprintf("%s/%s: %s", f.dir, f.name, fmt.Sprintf(format, a...))
}

// banOutside forbids a use or import everywhere except in the listed folders
// (a folder includes its subfolders and its tests).
func banOutside(name, what string, match func(f file) bool, allowed ...string) rule {
	return rule{name: name, check: func(f file) []string {
		if inDir(f, allowed...) || !match(f) {
			return nil
		}
		return []string{violation(f, "%s (only %s may)", what, strings.Join(allowed, ", "))}
	}}
}

// internalPrefix starts the import path of every package under internal/.
// The package lists of the rules below are written relative to it: "core"
// means internal/core, "agents/mock" means internal/agents/mock.
const internalPrefix = module + "internal/"

// banImports forbids a folder from importing certain internal packages (and
// their subpackages).
func banImports(name string, folder string, exceptTests bool, forbidden ...string) rule {
	return rule{name: name, check: func(f file) []string {
		if f.dir != folder || (exceptTests && f.test) {
			return nil
		}
		var out []string
		for _, imp := range f.imports {
			rel, ok := strings.CutPrefix(imp, internalPrefix)
			if !ok {
				continue
			}
			for _, bad := range forbidden {
				if rel == bad || strings.HasPrefix(rel, bad+"/") {
					out = append(out, violation(f, "imports internal/%s", rel))
				}
			}
		}
		return out
	}}
}

// onlyInternalImports allows a folder to import nothing from this module but
// the listed packages. Test files are left out: what a test imports is not a
// dependency of the shipped code.
func onlyInternalImports(name, folder string, allowed ...string) rule {
	return rule{name: name, check: func(f file) []string {
		if f.dir != folder || f.test {
			return nil
		}
		var out []string
		for _, imp := range f.imports {
			// Anything of this module that is not under internal/ (the web
			// package, say) is not allowed either.
			if !strings.HasPrefix(imp, module) {
				continue
			}
			rel := strings.TrimPrefix(strings.TrimPrefix(imp, internalPrefix), module)
			okImport := false
			for _, a := range allowed {
				if rel == a || strings.HasPrefix(rel, a+"/") {
					okImport = true
				}
			}
			if !okImport {
				out = append(out, violation(f, "imports %s", strings.TrimPrefix(imp, module)))
			}
		}
		return out
	}}
}

// rules are the architecture rules of docs/AGENTS.md sections 5 and 6.
var rules = []rule{
	// 5.3 Processes: every child process starts through platform.Start.
	banOutside("os/exec", "imports os/exec", func(f file) bool { return f.imports1("os/exec") }, "internal/platform"),
	banOutside("os.StartProcess", "calls os.StartProcess", func(f file) bool { return f.uses1("os.StartProcess") }, "internal/platform"),
	banOutside("syscall.StartProcess", "calls syscall.StartProcess", func(f file) bool { return f.uses1("syscall.StartProcess") }, "internal/platform"),
	banOutside("windows.CreateProcess", "calls windows.CreateProcess", func(f file) bool {
		return f.uses1("golang.org/x/sys/windows.CreateProcess") || f.uses1("golang.org/x/sys/windows.CreateProcessAsUser")
	}, "internal/platform"),

	// 5.2 Platform: no OS checks outside internal/platform.
	banOutside("runtime.GOOS", "reads runtime.GOOS", func(f file) bool { return f.uses1("runtime.GOOS") }, "internal/platform"),

	// 5.3 and invariant 3: the environment of a child comes from internal/safety.
	banOutside("os.Environ", "reads os.Environ", func(f file) bool { return f.uses1("os.Environ") }, "internal/safety"),
	banOutside("syscall.Environ", "reads syscall.Environ", func(f file) bool { return f.uses1("syscall.Environ") }, "internal/safety"),

	// Invariant 4: the audit insert that does not redact is for the audit
	// package only (and the database tests that check the triggers).
	banOutside("InsertAudit", "calls InsertAudit, which does not redact", func(f file) bool { return f.calls("InsertAudit") }, "internal/audit", "internal/db"),

	// 5.1: the data model imports nothing from the rest of Yggdrasil.
	onlyInternalImports("core is a leaf", "internal/core"),
	// Leaves that everything else builds on.
	onlyInternalImports("safety is a leaf", "internal/safety"),
	onlyInternalImports("platform is a leaf", "internal/platform"),
	// The database knows the data model and nothing above it.
	onlyInternalImports("db depends on core only", "internal/db", "core", "db"),
	// (the gen subfolder is sqlc's output and imports nothing of ours)
	onlyInternalImports("sqlc output is a leaf", "internal/db/gen"),
	// 5.5: no core, API or UI code depends on an adapter's output format. The
	// runner and the adapter contract know the data model only.
	onlyInternalImports("agents contract depends on core only", "internal/agents", "core"),
	banImports("api does not import an adapter", "internal/api", true, "agents/claudecode", "agents/mock"),
	banImports("audit does not import api or agents", "internal/audit", false, "api", "agents"),
	banImports("git does not import the layers above it", "internal/git", false, "api", "agents", "db"),
}
