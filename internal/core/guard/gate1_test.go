package guard

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// B2: program names and the resolved program.
func TestDecideProgramName(t *testing.T) {
	p := codePolicy()
	p.Allow = append(p.Allow, CommandRule{Program: "npm", Args: []string{"test"}, Tail: TailNone})
	for argv0, want := range map[string]bool{
		"go": true, "go.exe": true, "GO": true, "Go.EXE": true,
		"go.bat": false, "go.cmd": false, "go.com": false, "go.exe.bat": false, "go.exe.exe": false,
		"go.": false, "go ": false, "go.EXE.": false, "xgo": false,
	} {
		got := Decide(p, execCall([]string{argv0, "vet", "./..."})).Allow
		if got != want {
			t.Errorf("program %q allowed=%v, want %v", argv0, got, want)
		}
	}
	for argv0, want := range map[string]bool{"npm": true, "npm.exe": true, "npm.cmd": false, "npm.bat": false} {
		if got := Decide(p, execCall([]string{argv0, "test"})).Allow; got != want {
			t.Errorf("program %q allowed=%v, want %v", argv0, got, want)
		}
	}
}

func TestDecideProgramMustBeResolvedOutsideWriteRoots(t *testing.T) {
	argv := []string{"go", "vet", "./..."}
	mk := func(prog *PathRef) Call { return Call{Tool: ToolExec, Argv: argv, Program: prog} }
	cases := []struct {
		name string
		call Call
		want Reason // "" means allow
	}{
		{"resolved outside the write root", mk(&PathRef{Canon: `C:\tools\go\bin\go.exe`, OK: true}), ""},
		{"resolved inside a read-only root", mk(&PathRef{Canon: `C:\work\lib\bin\go.exe`, OK: true}), ""},
		{"program not resolved (nil)", mk(nil), ReasonNotAllowlisted},
		{"program not OK", mk(&PathRef{Canon: `C:\tools\go.exe`, OK: false}), ReasonPathUnresolved},
		{"program inside the worktree", mk(&PathRef{Canon: `C:\work\repo\go.exe`, OK: true}), ReasonNotAllowlisted},
		{"program inside the worktree, other case", mk(&PathRef{Canon: `c:\WORK\Repo\bin\go.bat`, OK: true}), ReasonNotAllowlisted},
		{"program in the write root itself", mk(&PathRef{Canon: `C:\work\repo`, OK: true}), ReasonNotAllowlisted},
		{"program with .. is not trusted", mk(&PathRef{Canon: `C:\tools\..\work\repo\go.exe`, OK: true}), ReasonNotAllowlisted},
		{"program with a relative path", mk(&PathRef{Canon: `go.exe`, OK: true}), ReasonNotAllowlisted},
		{"program empty", mk(&PathRef{OK: true}), ReasonNotAllowlisted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Decide(codePolicy(), c.call)
			if c.want == "" && !v.Allow {
				t.Fatalf("denied with %q", v.Reason)
			}
			if c.want != "" && (v.Allow || v.Reason != c.want) {
				t.Fatalf("got %+v, want Deny %q", v, c.want)
			}
		})
	}
}

// A program inside a write root is never allowed, whatever the rule says.
func TestPropProgramInWriteRootDenied(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 30; i++ {
			p := genPolicy(r)
			if len(p.WriteRoots) == 0 {
				continue
			}
			root := pick(r, p.WriteRoots...)
			prog := root + pick(r, `\go.exe`, `/bin/go.exe`, `\a\b\go`)
			if p.FoldCase && r.Intn(2) == 0 {
				prog = strings.ToUpper(prog)
			}
			p.Allow = append(p.Allow, CommandRule{Program: "go", Args: []string{"x"}, Tail: TailPaths})
			c := Call{Tool: ToolExec, Argv: []string{"go", "x"}, Program: &PathRef{Canon: prog, OK: true}}
			if Decide(p, c).Allow {
				t.Fatalf("allowed program %q inside write root %q", prog, root)
			}
		}
	})
}

// B3: only ASCII folds.
func TestDecideKelvinAndLongSLookAlikes(t *testing.T) {
	for _, c := range []struct{ root, canon string }{
		{`C:\work\kit`, "C:\\work\\\u212Ait"},          // Kelvin sign
		{`C:\work\sites`, "C:\\work\\\u017Fites"},      // long s
		{`C:\work\ǆ`, "C:\\work\\ǅ"},                   // DŽ title case
		{"C:\\work\\\u00e9", "C:\\work\\\u00c9"},       // é vs É
		{"C:\\work\\\u212Ait", "C:\\work\\kit"},        // and the other way round
		{"C:\\work\\caf\u00e9", "C:\\work\\CAF\u00c9"}, // non-ASCII must match byte-exactly
	} {
		p := Policy{Mode: core.ModeCode, ReadRoots: []string{c.root}, WriteRoots: []string{c.root}, FoldCase: true, Enforcement: EnforcementHook}
		for _, tool := range []Tool{ToolRead, ToolWrite} {
			if Decide(p, Call{Tool: tool, Paths: []PathRef{{Canon: c.canon + `\a`, OK: true}}}).Allow {
				t.Errorf("%s allowed %q under %q", tool, c.canon, c.root)
			}
		}
	}
	// ASCII still folds, and an identical non-ASCII segment still matches.
	p := Policy{Mode: core.ModeCode, ReadRoots: []string{"C:\\work\\caf\u00e9"}, FoldCase: true, Enforcement: EnforcementHook}
	for canon, want := range map[string]bool{"c:\\WORK\\caf\u00e9\\a": true, "C:\\work\\CAF\u00e9\\a": true, "C:\\work\\caf\u00c9\\a": false} {
		if got := Decide(p, Call{Tool: ToolRead, Paths: []PathRef{ref(canon)}}).Allow; got != want {
			t.Errorf("read %q allowed=%v, want %v", canon, got, want)
		}
	}
	// Protected names fold the same way.
	w := codePolicy()
	if Decide(w, Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\.CLAUDE\x`)}}).Allow {
		t.Error("ASCII-folded protected dir allowed")
	}
}

func TestAsciiFold(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"abc", "ABC", true}, {"K", "k", true}, {"\u212A", "k", false}, {"\u017F", "s", false},
		{"é", "É", false}, {"é", "é", true}, {"a", "ab", false}, {"", "", true}, {"@", "`", false}, {"[", "{", false},
	} {
		if got := asciiEqualFold(c.a, c.b); got != c.want {
			t.Errorf("asciiEqualFold(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// Read of .git/config and .git/hooks is denied (invariant: agent config and
// hooks are what the trust prompt guards).
func TestDecideReadOfGitConfigAndHooksDenied(t *testing.T) {
	p := codePolicy()
	for canon, want := range map[string]bool{
		`C:\work\repo\.git\config`:           false,
		`C:\work\repo\.GIT\CONFIG`:           false,
		`C:\work\repo\.git\hooks`:            false,
		`C:\work\repo\.git\hooks\pre-commit`: false,
		`C:\work\repo\sub\.git\hooks\x\y`:    false,
		`C:\work\repo\.git\modules\m\config`: true, // not a direct child; Read allowed
		`C:\work\repo\.git\HEAD`:             true,
		`C:\work\repo\.git\config.worktree`:  true,
		`C:\work\repo\.github\hooks\x`:       true,
		`C:\work\repo\src\config`:            true,
		`C:\work\repo\hooks\x`:               true,
		`C:\work\repo\.git`:                  true,
		`C:\work\repo\.git\objects\ab\cdef`:  true,
	} {
		for _, tool := range []Tool{ToolRead, ToolSearch} {
			v := Decide(p, Call{Tool: tool, Paths: []PathRef{ref(canon)}})
			if v.Allow != want || (!want && v.Reason != ReasonProtectedPath) {
				t.Errorf("%s %q: %+v, want allow=%v", tool, canon, v, want)
			}
		}
	}
	for glob, want := range map[string]bool{
		".git/hooks/*": false, "**/.git/config": false, `.git\hooks\**`: false, "src/**/*.go": true, ".git/HEAD": true,
	} {
		v := Decide(p, Call{Tool: ToolSearch, Globs: []string{glob}})
		if v.Allow != want {
			t.Errorf("glob %q: %+v, want allow=%v", glob, v, want)
		}
	}
}

func TestDecideProtectedNames(t *testing.T) {
	p := codePolicy()
	for _, rel := range []string{
		`.codex\config.toml`, `.gemini\settings.json`, `.cursor\rules\x`, `CLAUDE.local.md`, `sub\CLAUDE.local.md`,
		`.gitmodules`, `.husky\pre-commit`, `.githooks\pre-push`, `deep\.CODEX\x`,
	} {
		v := Decide(p, Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\` + rel)}})
		if v.Allow || v.Reason != ReasonProtectedPath {
			t.Errorf("write %q: %+v", rel, v)
		}
	}
	if !Decide(p, Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\docs\gitmodules.md`)}}).Allow {
		t.Error("a look-alike name was blocked")
	}
}

// ProtectedDirNames and ProtectedFileNames are what core/proposals must match.
func TestProtectedListsAreCopies(t *testing.T) {
	d := ProtectedDirNames()
	d[0] = "mutated"
	if ProtectedDirNames()[0] == "mutated" {
		t.Error("ProtectedDirNames returns the live slice")
	}
	f := ProtectedFileNames()
	f[0] = "mutated"
	if ProtectedFileNames()[0] == "mutated" {
		t.Error("ProtectedFileNames returns the live slice")
	}
}

func TestGlobsThatMatchParentFolder(t *testing.T) {
	p := codePolicy()
	for glob, want := range map[string]bool{
		`.[.]/x`: false, `[.][.]/x`: false, `.[!a]/x`: false, `.[.]`: false, `[.].`: false, `a[.]b`: true, `[ab].go`: true, `*.[ch]`: true, `.[a]`: true, `src/[a-z]*.go`: true, `.[!.]`: true,
	} {
		v := Decide(p, Call{Tool: ToolSearch, Globs: []string{glob}})
		if v.Allow != want {
			t.Errorf("glob %q allowed=%v, want %v", glob, v.Allow, want)
		}
	}
}

// Policy.Validate refuses roots that are a whole drive or the filesystem root.
func TestPolicyValidateRefusesBroadRoots(t *testing.T) {
	mk := func(root string) Policy {
		return Policy{Mode: core.ModeCode, ReadRoots: []string{root}, WriteRoots: []string{root}, Enforcement: EnforcementHook}
	}
	for _, root := range []string{
		`C:\`, `C:/`, `c:\`, `/`, `//`, `\`, `C:\.`, `C:\\`, `\\host`, `\\host\`, `\\host\share`, `\\host\share\`,
		`\\?\C:\`, `\\?\C:`, `\\?\`, `\\.\`,
	} {
		if err := mk(root).Validate(); err == nil {
			t.Errorf("root %q accepted", root)
		}
	}
	for _, root := range []string{`C:\work`, `C:/work`, `/srv`, `\\host\share\repo`, `\\?\C:\work`, `/a`} {
		if err := mk(root).Validate(); err != nil {
			t.Errorf("root %q refused: %v", root, err)
		}
	}
	// A read root too.
	p := mk(`C:\work\repo`)
	p.ReadRoots = append(p.ReadRoots, `D:\`)
	if p.Validate() == nil {
		t.Error("a drive as a read root accepted")
	}
}
