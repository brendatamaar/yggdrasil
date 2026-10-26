package safety_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func TestAllowlistRoundTrip(t *testing.T) {
	rules := []guard.CommandRule{
		{Program: "go", Args: []string{"vet", "./..."}, Tail: guard.TailNone},
		{Program: "npm", Args: []string{"run", "test"}, Tail: guard.TailPaths},
		{Program: "mvn", Tail: guard.TailPaths},
		{Program: "x", Args: []string{"two words"}, Tail: guard.TailNone},
	}
	data := safety.FormatAllowlist(rules)
	if data == nil {
		t.Fatal("format failed")
	}
	got, err := safety.ParseAllowlist(data)
	if err != nil || !reflect.DeepEqual(got, rules) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := safety.ParseAllowlist([]byte("[]")); err != nil || len(got) != 0 {
		t.Fatalf("empty list: %v %v", got, err)
	}
}

func TestAllowlistRejects(t *testing.T) {
	cases := map[string]string{
		"not json":            `nope`,
		"not an array":        `{"program":"go"}`,
		"unknown field":       `[{"program":"go","args":[],"tail":"none","extra":1}]`,
		"trailing data":       `[] []`,
		"empty program":       `[{"program":"","tail":"none"}]`,
		"slash in program":    `[{"program":"bin/go","tail":"none"}]`,
		"backslash":           `[{"program":"bin\\go","tail":"none"}]`,
		"drive path":          `[{"program":"C:go","tail":"none"}]`,
		"option as program":   `[{"program":"-go","tail":"none"}]`,
		"dot program":         `[{"program":"..","tail":"none"}]`,
		"space in program":    `[{"program":"g o","tail":"none"}]`,
		"shell char program":  `[{"program":"go;rm","tail":"none"}]`,
		"unknown tail":        `[{"program":"go","tail":"any"}]`,
		"empty tail":          `[{"program":"go","tail":""}]`,
		"dollar in arg":       `[{"program":"go","args":["$HOME"],"tail":"none"}]`,
		"backslash in arg":    `[{"program":"go","args":["a\\b"],"tail":"none"}]`,
		"glob in arg":         `[{"program":"go","args":["*"],"tail":"none"}]`,
		"comma in arg":        `[{"program":"go","args":["a,b"],"tail":"none"}]`,
		"empty arg":           `[{"program":"go","args":[""],"tail":"none"}]`,
		"control char in arg": `[{"program":"go","args":["a\u0001"],"tail":"none"}]`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := safety.ParseAllowlist([]byte(in)); err == nil {
				t.Fatalf("accepted %s as %+v", in, got)
			}
		})
	}
	var many []string
	for range safety.MaxAllowlistRules + 1 {
		many = append(many, `{"program":"go","tail":"none"}`)
	}
	if _, err := safety.ParseAllowlist([]byte("[" + strings.Join(many, ",") + "]")); err == nil {
		t.Fatal("accepted too many rules")
	}
}

func TestValidateRuleRefusesDangerousPrograms(t *testing.T) {
	always := []string{"sh", "bash", "zsh", "dash", "fish", "cmd", "powershell", "pwsh", "wscript", "cscript", "mshta",
		"git", "ssh", "scp", "curl", "wget", "nc", "ncat", "telnet", "ftp", "rm", "del", "rmdir", "format", "reg", "sc", "net", "runas"}
	for _, p := range always {
		for _, name := range []string{p, strings.ToUpper(p), p + ".exe", strings.ToUpper(p) + ".CMD"} {
			t.Run(name, func(t *testing.T) {
				for _, r := range []guard.CommandRule{
					{Program: name, Tail: guard.TailNone},
					{Program: name, Args: []string{"x"}, Tail: guard.TailNone},
					{Program: name, Args: []string{"x"}, Tail: guard.TailPaths},
				} {
					if err := safety.ValidateRule(r); err == nil {
						t.Fatalf("accepted %+v", r)
					}
				}
			})
		}
	}
}

func TestValidateRuleInterpreters(t *testing.T) {
	for _, p := range []string{"node", "python", "python3", "py", "ruby", "perl", "php", "deno", "bun", "Node.exe"} {
		t.Run(p, func(t *testing.T) {
			bad := []guard.CommandRule{
				{Program: p, Tail: guard.TailNone},                           // no fixed args
				{Program: p, Args: []string{"build.js"}, Tail: guard.TailPaths}, // open tail
				{Program: p, Tail: guard.TailPaths},
			}
			for _, r := range bad {
				if err := safety.ValidateRule(r); err == nil {
					t.Fatalf("accepted %+v", r)
				}
			}
			if err := safety.ValidateRule(guard.CommandRule{Program: p, Args: []string{"build.js"}, Tail: guard.TailNone}); err != nil {
				t.Fatalf("refused a fixed script: %v", err)
			}
		})
	}
}

func TestValidateRuleWrappersUnsupported(t *testing.T) {
	for _, p := range []string{"mvnw", "gradlew", "MVNW.cmd", "gradlew.bat"} {
		err := safety.ValidateRule(guard.CommandRule{Program: p, Args: []string{"verify"}, Tail: guard.TailNone})
		if err == nil || !strings.Contains(err.Error(), "not supported yet") {
			t.Fatalf("%s: err = %v", p, err)
		}
	}
}

func FuzzParseAllowlist(f *testing.F) {
	f.Add([]byte(`[{"program":"go","args":["vet"],"tail":"paths"}]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`[{"program":"a/b","tail":"none"}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		rules, err := safety.ParseAllowlist(data)
		if err != nil {
			return
		}
		for _, r := range rules {
			// An independent predicate, not ValidateRule itself.
			words := append([]string{r.Program}, r.Args...)
			for _, w := range words {
				argv, err := guard.ParseCommandLine(guard.FormatCommandLine([]string{w}))
				if w == "" || err != nil || len(argv) != 1 || argv[0] != w {
					t.Fatalf("accepted %q that does not survive the command-line rules in %+v", w, r)
				}
			}
			if strings.ContainsAny(r.Program, `/\ `) {
				t.Fatalf("program %q has a separator", r.Program)
			}
			if r.Tail != guard.TailNone && r.Tail != guard.TailPaths {
				t.Fatalf("accepted tail %q", r.Tail)
			}
		}
		again, err := safety.ParseAllowlist(safety.FormatAllowlist(rules))
		if err != nil || !reflect.DeepEqual(again, rules) {
			t.Fatalf("round trip: %+v vs %+v (%v)", again, rules, err)
		}
	})
}

func FuzzReadPolicyFile(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"version":1}`))
	f.Add([]byte(``))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		file := filepath.Join(dir, "policy.json")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		p, err := safety.ReadPolicyFile(file)
		if err != nil {
			return
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("accepted an invalid policy: %v", err)
		}
	})
}

func FuzzSelectConfig(f *testing.F) {
	f.Add("a/.git/CLAUDE.md", ".claude/agents", "CLAUDE.md")
	f.Add(".CLAUDE/Agents/x.md", ".claude/agents", "CLAUDE.md")
	f.Add("sub/claude.md", "", "CLAUDE.md")
	f.Fuzz(func(t *testing.T, rel, cfgPath, name string) {
		got := safety.SelectConfig(rel, []string{cfgPath}, []string{name})
		for _, seg := range strings.Split(rel, "/") {
			if strings.EqualFold(seg, ".git") && got {
				t.Fatalf("selected %q inside .git", rel)
			}
		}
		ascii := func(s string) bool {
			for i := 0; i < len(s); i++ {
				if s[i] >= 0x80 {
					return false
				}
			}
			return true
		}
		if ascii(rel) && ascii(cfgPath) && ascii(name) {
			up := safety.SelectConfig(strings.ToUpper(rel), []string{strings.ToLower(cfgPath)}, []string{strings.ToUpper(name)})
			if up != got {
				t.Fatalf("case changes the answer for %q %q %q", rel, cfgPath, name)
			}
		}
	})
}

func FuzzBuildScanSummary(f *testing.F) {
	f.Add([]byte(`{"hooks":{"PreToolUse":[{"command":"x"}]},"mcpServers":{"a":{"command":"npx","args":["y"]}}}`))
	f.Add([]byte(`{nope`))
	f.Add([]byte("{\"mcp\":{\"s\":{\"url\":\"https://u:pw@h/x\"}}}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		sc := safety.BuildScan([]safety.ScanInput{{Path: ".mcp.json", Content: data}})
		for _, sf := range sc.Files {
			for _, l := range sf.WouldRun {
				if strings.ContainsAny(l, "\r\n\x00") {
					t.Fatalf("summary line is not one line: %q", l)
				}
			}
		}
	})
}

func testPolicy(t *testing.T) guard.Policy {
	t.Helper()
	root, err := platform.ResolveRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return guard.Policy{
		Mode: core.ModeAsk, ReadRoots: []string{root},
		Allow:       []guard.CommandRule{{Program: "go", Args: []string{"vet"}, Tail: guard.TailNone}},
		FoldCase:    platform.FoldCase(),
		Enforcement: guard.EnforcementHook,
	}
}

func TestPolicyFileRoundTrip(t *testing.T) {
	p := testPolicy(t)
	parent := t.TempDir()
	path, err := safety.WritePolicyFile(parent, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, parent) {
		t.Fatalf("file outside the parent: %s", path)
	}
	got, err := safety.ReadPolicyFile(path)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("got %+v, %v", got, err)
	}
	// Each run gets its own folder; nothing is overwritten.
	path2, err := safety.WritePolicyFile(parent, p)
	if err != nil || filepath.Dir(path2) == filepath.Dir(path) {
		t.Fatalf("second file shares the folder: %s %s %v", path, path2, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm()&0o077 != 0 && fi.Mode().Perm() != 0o666 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
}

func TestPolicyFileRefusesInvalid(t *testing.T) {
	p := testPolicy(t)
	p.ReadRoots = nil
	if _, err := safety.WritePolicyFile(t.TempDir(), p); err == nil {
		t.Fatal("wrote an invalid policy")
	}
	bad := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(bad, []byte(`{"mode":"ask"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := safety.ReadPolicyFile(bad); err == nil {
		t.Fatal("read an invalid policy")
	}
	if _, err := safety.ReadPolicyFile(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Fatal("read a missing file")
	}
}
