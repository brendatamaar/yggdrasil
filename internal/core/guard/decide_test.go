package guard

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func goProgram() *PathRef { return &PathRef{Canon: `C:\tools\go\bin\go.exe`, OK: true} }

// execCall is an Exec call whose program resolved outside every write root.
func execCall(argv []string) Call { return Call{Tool: ToolExec, Argv: argv, Program: goProgram()} }

func withParseErr(c Call) Call { c.ParseErr = true; return c }

func ref(p string) PathRef { return PathRef{Canon: p, OK: true, Raw: p} }

func codePolicy() Policy {
	return Policy{
		Mode:        core.ModeCode,
		ReadRoots:   []string{`C:\work\repo`, `C:\work\lib`},
		WriteRoots:  []string{`C:\work\repo`},
		FoldCase:    true,
		Enforcement: EnforcementHook,
		Allow: []CommandRule{
			{Program: "go", Args: []string{"vet", "./..."}, Tail: TailNone},
			{Program: "go", Args: []string{"test"}, Tail: TailPaths},
			{Program: "mvn", Args: []string{"verify"}, Tail: TailNone},
		},
	}
}

func askPolicy() Policy {
	p := codePolicy()
	p.Mode = core.ModeAsk
	p.WriteRoots = nil
	return p
}

// TestDecideRows covers every row D1 to D15 at least once.
func TestDecideRows(t *testing.T) {
	cases := []struct {
		name   string
		policy Policy
		call   Call
		want   Reason // "" means Allow
	}{
		{"D1 unknown tool value", codePolicy(), Call{Tool: "bogus"}, ReasonUnknownTool},
		{"D1 empty tool", codePolicy(), Call{}, ReasonUnknownTool},
		{"D1 unknown mode", func() Policy { p := codePolicy(); p.Mode = "yolo"; return p }(),
			Call{Tool: ToolRead, Paths: []PathRef{ref(`C:\work\repo\a`)}}, ReasonUnknownTool},
		{"D2 unresolved read", codePolicy(), Call{Tool: ToolRead, Paths: []PathRef{{Canon: `C:\work\repo\a`, OK: false}}}, ReasonPathUnresolved},
		{"D2 unresolved exec path", codePolicy(), func() Call { c := execCall([]string{"go", "test"}); c.Paths = []PathRef{{OK: false}}; return c }(), ReasonPathUnresolved},
		{"D3 read inside", codePolicy(), Call{Tool: ToolRead, Paths: []PathRef{ref(`C:\work\repo\src\a.go`)}}, ""},
		{"D3 read the root itself", codePolicy(), Call{Tool: ToolRead, Paths: []PathRef{ref(`C:\work\repo`)}}, ""},
		{"D3 read in second read root", codePolicy(), Call{Tool: ToolRead, Paths: []PathRef{ref(`C:\work\lib\x`)}}, ""},
		{"D3 search with glob", codePolicy(), Call{Tool: ToolSearch, Paths: []PathRef{ref(`C:\work\repo`)}, Globs: []string{"**/*.go"}}, ""},
		{"D3 search with absolute glob inside", codePolicy(), Call{Tool: ToolSearch, Globs: []string{`C:\work\repo\src\**\*.go`}}, ""},
		{"D4 read outside", codePolicy(), Call{Tool: ToolRead, Paths: []PathRef{ref(`C:\Users\someone\secrets.txt`)}}, ReasonOutsideRepo},
		{"D4 read one outside among two", codePolicy(), Call{Tool: ToolRead, Paths: []PathRef{ref(`C:\work\repo\a`), ref(`C:\extra\b`)}}, ReasonOutsideRepo},
		{"D4 glob with ..", codePolicy(), Call{Tool: ToolSearch, Globs: []string{`..\*`}}, ReasonOutsideRepo},
		{"D4 absolute glob outside", codePolicy(), Call{Tool: ToolSearch, Globs: []string{`C:\Windows\**`}}, ReasonOutsideRepo},
		{"D4 glob alternative leaves the repo", codePolicy(), Call{Tool: ToolSearch, Globs: []string{`{/etc,x}`}}, ReasonOutsideRepo},
		{"D4 home glob", codePolicy(), Call{Tool: ToolSearch, Globs: []string{`~/.ssh/*`}}, ReasonOutsideRepo},
		{"D4 search prefix sibling", codePolicy(), Call{Tool: ToolSearch, Paths: []PathRef{ref(`C:\work\repo-evil`)}}, ReasonOutsideRepo},
		{"D5 write in ask", askPolicy(), Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\a`)}}, ReasonAskIsReadOnly},
		{"D6 write .git", codePolicy(), Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\.git\config`)}}, ReasonProtectedPath},
		{"D6 write agent config", codePolicy(), Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\.claude\settings.json`)}}, ReasonProtectedPath},
		{"D6 write CLAUDE.md case-insensitive", codePolicy(), Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\sub\claude.md`)}}, ReasonProtectedPath},
		{"D7 write inside", codePolicy(), Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\src\a.go`)}}, ""},
		{"D7 write a file named like a protected dir prefix", codePolicy(), Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\repo\.github\ci.yml`)}}, ""},
		{"D8 write outside", codePolicy(), Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\extra\a`)}}, ReasonOutsideWorktree},
		{"D8 write readable but not writable", codePolicy(), Call{Tool: ToolWrite, Paths: []PathRef{ref(`C:\work\lib\a`)}}, ReasonOutsideWorktree},
		{"D8 write naming no path", codePolicy(), Call{Tool: ToolWrite}, ReasonOutsideWorktree},
		{"D9 exec in ask", askPolicy(), execCall([]string{"go", "test"}), ReasonAskIsReadOnly},
		{"D10 exec with shell syntax", codePolicy(), withParseErr(execCall([]string{"go", "test"})), ReasonShellSyntax},
		{"D11 exec matches", codePolicy(), execCall([]string{"go", "vet", "./..."}), ""},
		{"D11 exec .exe and case", codePolicy(), execCall([]string{"GO.EXE", "vet", "./..."}), ""},
		{"D12 exec wrapper .cmd is not the bare name", codePolicy(), execCall([]string{"mvnw.cmd", "verify"}), ReasonNotAllowlisted},
		{"D11 exec tail path inside", codePolicy(), execCall([]string{"go", "test", `C:/work/repo/pkg`, "./pkg"}), ""},
		{"D12 exec not listed", codePolicy(), execCall([]string{"git", "push"}), ReasonNotAllowlisted},
		{"D12 exec extra arg without tail rule", codePolicy(), execCall([]string{"go", "vet", "./...", "extra"}), ReasonNotAllowlisted},
		{"D12 exec missing fixed arg", codePolicy(), execCall([]string{"go", "vet"}), ReasonNotAllowlisted},
		{"D12 exec flag in tail", codePolicy(), execCall([]string{"go", "test", "-run", "X"}), ReasonNotAllowlisted},
		{"D12 exec response file in tail", codePolicy(), execCall([]string{"go", "test", "@args.txt"}), ReasonNotAllowlisted},
		{"D12 exec tail path outside", codePolicy(), execCall([]string{"go", "test", `C:\elsewhere\x`}), ReasonNotAllowlisted},
		{"D12 exec tail parent escape", codePolicy(), execCall([]string{"go", "test", `../x`}), ReasonNotAllowlisted},
		{"D12 exec program with folder", codePolicy(), execCall([]string{`.\go`, "vet", "./..."}), ReasonNotAllowlisted},
		{"D12 exec program with full path", codePolicy(), execCall([]string{`C:\evil\go.exe`, "vet", "./..."}), ReasonNotAllowlisted},
		{"D12 exec no argv", codePolicy(), Call{Tool: ToolExec, Program: goProgram()}, ReasonNotAllowlisted},
		{"D12 exec empty allowlist", func() Policy { p := codePolicy(); p.Allow = nil; return p }(), execCall([]string{"go", "vet", "./..."}), ReasonNotAllowlisted},
		{"D13 sub-agent isolation", codePolicy(), Call{Tool: ToolSubagent, Isolation: "worktree"}, ReasonAgentWorktreeBlocked},
		{"D14 sub-agent", codePolicy(), Call{Tool: ToolSubagent}, ""},
		{"D14 sub-agent in ask", askPolicy(), Call{Tool: ToolSubagent}, ""},
		{"D15 extra tool", codePolicy(), Call{Tool: ToolOther, ToolName: "WebFetch"}, ReasonUnknownTool},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Decide(c.policy, c.call)
			if c.want == "" {
				if !v.Allow {
					t.Fatalf("denied with %q, want Allow", v.Reason)
				}
				return
			}
			if v.Allow || v.Reason != c.want {
				t.Fatalf("got %+v, want Deny %q", v, c.want)
			}
		})
	}
}

func TestDecideWindowsPaths(t *testing.T) {
	read := func(p Policy, canon string) bool {
		return Decide(p, Call{Tool: ToolRead, Paths: []PathRef{ref(canon)}}).Allow
	}
	p := codePolicy() // FoldCase true, root C:\work\repo
	cs := p
	cs.FoldCase = false
	cases := []struct {
		name   string
		policy Policy
		canon  string
		want   bool
	}{
		{"same case", p, `C:\work\repo\a`, true},
		{"folded case", p, `c:\WORK\Repo\A`, true},
		{"case differs, case-sensitive policy", cs, `c:\WORK\Repo\A`, false},
		{"forward slashes", p, `C:/work/repo/a`, true},
		{"mixed slashes", p, `C:\work/repo\a/b`, true},
		{"trailing separator", p, `C:\work\repo\`, true},
		{"doubled separators", p, `C:\\work\\repo\\a`, true},
		{"dot part", p, `C:\work\.\repo\a`, true},
		{"prefix sibling", p, `C:\work\repo-evil`, false},
		{"prefix sibling file", p, `C:\work\repo-evil\a.txt`, false},
		{"prefix sibling digit", p, `C:\work\repo2\a`, false},
		{"parent", p, `C:\work`, false},
		{"parent escape", p, `C:\work\repo\..\extra\a`, false},
		{"extra drive", p, `D:\work\repo\a`, false},
		{"8.3 look-alike", p, `C:\work\REPO~1\a`, false},
		{"8.3 look-alike of root", p, `C:\WORK~1\repo\a`, false},
		{"alternate data stream", p, `C:\work\repo\a.txt:evil`, false},
		{"drive-relative", p, `C:work\repo\a`, false},
		{"trailing dot trimmed by Windows", p, `C:\work\repo\a.`, false},
		{"trailing space trimmed by Windows", p, `C:\work\repo\a `, false},
		{"UNC form of the same folder", p, `\\localhost\c$\work\repo\a`, false},
		{"extended-length prefix", p, `\\?\C:\work\repo\a`, false},
		{"rooted without drive", p, `\work\repo\a`, false},
		{"relative", p, `work\repo\a`, false},
		{"empty", p, ``, false},
		{"NUL byte", p, "C:\\work\\repo\\a\x00.txt", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := read(c.policy, c.canon); got != c.want {
				t.Fatalf("read %q allowed=%v, want %v", c.canon, got, c.want)
			}
		})
	}
}

func TestDecideUnixPaths(t *testing.T) {
	p := Policy{Mode: core.ModeCode, ReadRoots: []string{"/srv/repo"}, WriteRoots: []string{"/srv/repo"}, Enforcement: EnforcementHook}
	for canon, want := range map[string]bool{
		"/srv/repo": true, "/srv/repo/a/b": true, "/srv/repo-evil/a": false, "/srv/Repo/a": false,
		"/srv/repo/../x": false, "/srv": false, "srv/repo/a": false, "//srv/repo/a": false,
	} {
		if got := Decide(p, Call{Tool: ToolWrite, Paths: []PathRef{ref(canon)}}).Allow; got != want {
			t.Errorf("write %q allowed=%v, want %v", canon, got, want)
		}
	}
}

func TestDecideDoesNotLeakPathInDenial(t *testing.T) {
	v := Decide(codePolicy(), Call{Tool: ToolRead, Paths: []PathRef{ref(`C:\secret\path`)}})
	if v.Allow || v.Reason.Message() == "" {
		t.Fatalf("unexpected %+v", v)
	}
}
