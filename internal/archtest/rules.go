package archtest

import (
	"fmt"
	"slices"
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

// importedOnlyBy lets only the listed folders (and their subfolders) import
// pkg, a path relative to internal/. With testsOnly, _test.go files anywhere
// may import it too.
func importedOnlyBy(name, pkg string, testsOnly bool, allowed ...string) rule {
	return rule{name: name, check: func(f file) []string {
		if inDir(f, allowed...) || (testsOnly && f.test) {
			return nil
		}
		for _, imp := range f.imports {
			if rel, ok := strings.CutPrefix(imp, internalPrefix); ok && (rel == pkg || strings.HasPrefix(rel, pkg+"/")) {
				return []string{violation(f, "imports internal/%s", rel)}
			}
		}
		return nil
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

// corePureStdlib is what the functional core may import from the standard
// library. Paths match exactly, so "math" does not admit math/rand. time is
// here for its types (time.Time, time.Duration); clock reads are the linter's job.
var corePureStdlib = []string{
	"cmp", "crypto/sha256", "encoding/hex", "encoding/json", "errors", "fmt", "maps", "math", "math/bits",
	"slices", "sort", "strconv", "strings", "time", "unicode", "unicode/utf8",
}

// corePure keeps internal/core and its subpackages free of effects: pure
// standard library packages and other core packages only.
var corePure = rule{name: "core imports only pure packages", check: func(f file) []string {
	if !inDir(f, "internal/core") || f.test {
		return nil
	}
	var out []string
	for _, imp := range f.imports {
		if slices.Contains(corePureStdlib, imp) || imp == internalPrefix+"core" || strings.HasPrefix(imp, internalPrefix+"core/") {
			continue
		}
		out = append(out, violation(f, "imports %q, which the functional core may not (allowed: %s and internal/core/...); move the effect to the owning package and return a command (docs/AGENTS.md 5.11)", imp, strings.Join(corePureStdlib, ", ")))
	}
	return out
}}

// coreSubpackages keeps each domain package under internal/core a leaf on
// top of internal/core: it may import internal/core and nothing else of this
// module, not even a sibling domain, so domains stay independent.
var coreSubpackages = rule{name: "core domains import only core", check: func(f file) []string {
	if !strings.HasPrefix(f.dir, "internal/core/") || f.test {
		return nil
	}
	var out []string
	for _, imp := range f.imports {
		if strings.HasPrefix(imp, module) && imp != internalPrefix+"core" {
			out = append(out, violation(f, "imports %s; a domain package under internal/core may import only internal/core", strings.TrimPrefix(imp, module)))
		}
	}
	return out
}}

// storeAllowed reports whether name may be an exported method of db.Store.
func storeAllowed(name string) bool {
	return name == "Commit" || name == "AppendTaskEvent" || name == "TaskDependencies" ||
		strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "List")
}

// auditForwarders are the files that call AppendAudit without making an entry
// themselves, so audit.Prepare does not apply to them (invariant 4):
//   - internal/verify/store.go only forwards the call of the service, whose
//     entries are made with audit.Prepare in service.go and actions.go;
//   - internal/verify/verifytest/contract.go writes fixture entries to the
//     store under test, to check that the store keeps them as given.
//
// Every other file stays under the rule. Production files are not exempted
// without a reason written here.
var auditForwarders = map[string]bool{
	"internal/verify/store.go":               true,
	"internal/verify/verifytest/contract.go": true,
}

// storeWrites forbids an exported write method on db.Store other than the
// allowed set. Together with "db/gen only from db" and the unexported DB
// pools, no package outside internal/db can write without Store.Commit.
// (Commit itself doesn't force an AppendAudit; callers must add one.)
var storeWrites = rule{name: "Store exports no un-audited write", check: func(f file) []string {
	if f.dir != "internal/db" || f.test {
		return nil
	}
	var out []string
	for _, m := range f.storeMethods {
		if !storeAllowed(m) {
			out = append(out, violation(f, "Store.%s is exported; writes go through Store.Commit with an audit entry (allowed: Commit, AppendTaskEvent, Get*, List*, TaskDependencies)", m))
		}
	}
	return out
}}

// deleteFreeDirs are the Phase 4 surfaces (verification, landing, change sets,
// API, MCP, wiring) where no code deletes anything. internal/proposals and
// internal/git have their own older rule. The one deletion in the product is
// platform.RemoveStaged, below <root>/.removing/<leaf>.
var deleteFreeDirs = []string{"internal/verify", "internal/changesets", "internal/composer", "internal/core", "internal/api", "internal/mcp", "internal/db", "cmd/ygg"}

// deleteAllowed are production files in those dirs with a documented reason
// to remove a file of their own.
var deleteAllowed = map[string]bool{
	"cmd/ygg/doctor.go":                true, // removes the doctor's own empty probe file
	"internal/core/guard/integrity.go": true, // the deny list: names these commands to refuse them
	"internal/core/guard/rule.go":      true,
}

// phase4NoDelete is invariant 1 for the Phase 4 surface: no os.Remove-family
// call, no rm/rmdir/del command, no git clean/reset --hard/branch -D/worktree remove.
var phase4NoDelete = rule{name: "phase 4 code never deletes", check: func(f file) []string {
	if f.test || !inDir(f, deleteFreeDirs...) || deleteAllowed[f.dir+"/"+f.name] {
		return nil
	}
	var out []string
	for _, call := range []string{"os.Remove", "os.RemoveAll", "os.Truncate", "syscall.Unlink", "golang.org/x/sys/windows.DeleteFile"} {
		if f.uses1(call) {
			out = append(out, violation(f, "calls %s; only platform.RemoveStaged deletes, below <root>/.removing (invariant 1)", call))
		}
	}
	for _, l := range f.literals {
		switch strings.TrimSpace(l) {
		case "rm", "rmdir", "del", "erase", "rd", "clean", "--hard", "-D", "--force", "-f", "Remove-Item":
			out = append(out, violation(f, "string %q names a deleting command or a destructive git flag (invariant 1)", l))
		}
	}
	return out
}}

// rules are the architecture rules of docs/AGENTS.md sections 5 and 6.
var rules = []rule{
	// 5.3 Processes: every child process starts through platform.Start.
	banOutside("os/exec", "imports os/exec", func(f file) bool { return f.imports1("os/exec") }, "internal/platform"),
	banOutside("os.StartProcess", "calls os.StartProcess", func(f file) bool { return f.uses1("os.StartProcess") }, "internal/platform"),
	banOutside("syscall.StartProcess", "calls syscall.StartProcess", func(f file) bool { return f.uses1("syscall.StartProcess") }, "internal/platform"),
	banOutside("windows.CreateProcess", "calls windows.CreateProcess", func(f file) bool {
		return f.uses1("golang.org/x/sys/windows.CreateProcess") || f.uses1("golang.org/x/sys/windows.CreateProcessAsUser")
	}, "internal/platform"),

	// Invariant 1: Yggdrasil never deletes files. Executing a proposal only
	// moves (os.Rename); the code that runs proposals and the git wrapper
	// have no way to remove anything. Tests may clean up after themselves.
	{name: "proposals and git never delete files", check: func(f file) []string {
		if f.test || !inDir(f, "internal/proposals", "internal/git") {
			return nil
		}
		var out []string
		for _, call := range []string{"os.Remove", "os.RemoveAll", "os.Truncate", "os.O_TRUNC", "syscall.O_TRUNC", "syscall.Unlink", "golang.org/x/sys/windows.DeleteFile"} {
			if f.uses1(call) {
				out = append(out, violation(f, "calls %s; proposals move files into _to_delete/ and never delete (invariant 1)", call))
			}
		}
		return out
	}},

	phase4NoDelete,

	// 5.2 Platform: no OS checks outside internal/platform.
	banOutside("runtime.GOOS", "reads runtime.GOOS", func(f file) bool { return f.uses1("runtime.GOOS") }, "internal/platform"),

	// 5.3 and invariant 3: the environment of a child comes from internal/safety.
	banOutside("os.Environ", "reads os.Environ", func(f file) bool { return f.uses1("os.Environ") }, "internal/safety"),
	banOutside("syscall.Environ", "reads syscall.Environ", func(f file) bool { return f.uses1("syscall.Environ") }, "internal/safety"),

	// Invariant 4: Tx.AppendAudit writes the entry as given and does not
	// redact. Outside the database package (whose tests check the triggers)
	// and the audit package (which calls Prepare unqualified), a file that
	// appends audit entries must make them with audit.Prepare.
	banOutside("AppendAudit", "calls AppendAudit without audit.Prepare, which redacts", func(f file) bool {
		return f.calls("AppendAudit") && !f.uses1(internalPrefix+"audit.Prepare") && !auditForwarders[f.dir+"/"+f.name]
	}, "internal/db", "internal/audit"),

	// Invariant 8: every write of state goes through Store.Commit, with its
	// audit entry in the same transaction. Store exports no other write:
	// reads (Get*, List*, TaskDependencies), Commit, and AppendTaskEvent (the
	// transcript is not audited by design). Test helpers live in _test.go
	// files and in internal/db/dbtest.
	storeWrites,
	// Invariant 8: the raw sqlc queries write without an audit entry, so only
	// the database package uses them; everyone else goes through Store.Commit.
	importedOnlyBy("db/gen only from db", "db/gen", false, "internal/db"),
	// Test helpers write without audit entries and invent IDs and times.
	importedOnlyBy("dbtest only from tests", "db/dbtest", true),
	importedOnlyBy("platformtest only from tests", "platform/platformtest", true, "internal/platform/platformtest"),

	// 5.1: the data model imports nothing from the rest of Yggdrasil.
	onlyInternalImports("core is a leaf", "internal/core"),
	// 5.11: the functional core has no effects.
	corePure,
	coreSubpackages,
	// Leaves that everything else builds on.
	onlyInternalImports("safety depends on core and platform only", "internal/safety", "core", "platform"),
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
