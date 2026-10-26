package guard_test

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

var refusedGroups = map[string][]string{
	"shells":      {"sh", "bash", "zsh", "dash", "fish", "cmd", "powershell", "pwsh", "powershell_ise", "wscript", "cscript", "mshta", "wsl", "env", "xargs", "find", "start", "explorer"},
	"git":         {"git"},
	"network":     {"ssh", "scp", "curl", "wget", "nc", "ncat", "telnet", "ftp", "gh", "glab", "docker"},
	"destructive": {"rm", "del", "erase", "rmdir", "format", "reg", "sc", "net", "runas", "mklink", "icacls", "takeown", "msiexec", "rundll32", "regsvr32", "certutil", "bitsadmin", "schtasks", "wmic", "robocopy", "xcopy", "move", "copy", "tar", "7z"},
	"wrappers":    {"mvnw", "gradlew"},
}

var (
	interpreterNames = []string{"node", "npx", "tsx", "ts-node", "python", "python3", "pythonw", "py", "ruby", "perl", "php", "deno", "bun"}
	pkgTools         = []string{"npm", "pnpm", "yarn", "pip", "pip3", "uv", "cargo", "go", "dotnet", "mvn", "gradle"}
	badSub           = []string{"install", "add", "i", "publish", "exec", "dlx", "x", "remove", "uninstall", "update", "upgrade"}
)

func variant(n string, r *rand.Rand) string {
	s := n
	if r.Intn(2) == 0 {
		s = strings.ToUpper(s)
	}
	switch r.Intn(4) {
	case 0:
		s += ".exe"
	case 1:
		s += ".CMD"
	}
	return s
}

func TestRuleRefusedGroups(t *testing.T) {
	for g, names := range refusedGroups {
		t.Run(g, func(t *testing.T) {
			for _, n := range names {
				for _, p := range []string{n, strings.ToUpper(n), n + ".exe", strings.ToUpper(n) + ".CMD"} {
					for _, args := range [][]string{nil, {"run", "x"}} {
						r := guard.CommandRule{Program: p, Args: args, Tail: guard.TailNone}
						if r.Validate() == nil {
							t.Errorf("rule %q %v accepted", p, args)
						}
					}
				}
			}
		})
	}
}

func TestRuleInterpreters(t *testing.T) {
	for _, n := range interpreterNames {
		for _, r := range []guard.CommandRule{
			{Program: n, Tail: guard.TailNone},
			{Program: n, Args: []string{"a.js"}, Tail: guard.TailPaths},
		} {
			if r.Validate() == nil {
				t.Errorf("%+v accepted", r)
			}
		}
		if err := (guard.CommandRule{Program: n, Args: []string{"build.js"}, Tail: guard.TailNone}).Validate(); err != nil {
			t.Errorf("%s fixed script refused: %v", n, err)
		}
	}
}

func TestRulePackageManagers(t *testing.T) {
	for _, n := range pkgTools {
		if (guard.CommandRule{Program: n, Tail: guard.TailPaths}).Validate() == nil {
			t.Errorf("%s without fixed arg accepted", n)
		}
		for _, s := range badSub {
			if (guard.CommandRule{Program: n, Args: []string{s}, Tail: guard.TailNone}).Validate() == nil {
				t.Errorf("%s %s accepted", n, s)
			}
		}
		if err := (guard.CommandRule{Program: n, Args: []string{"test"}, Tail: guard.TailPaths}).Validate(); err != nil {
			t.Errorf("%s test refused: %v", n, err)
		}
	}
}

func TestPolicyNeverHoldsListedRule(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	var all []string
	for _, g := range refusedGroups {
		all = append(all, g...)
	}
	all = append(all, interpreterNames...)
	all = append(all, pkgTools...)
	args := append(append([]string{}, badSub...), "test", "run")
	for i := 0; i < 3000; i++ {
		rule := guard.CommandRule{Program: variant(all[r.Intn(len(all))], r), Tail: guard.TailRule([]string{"none", "paths"}[r.Intn(2)])}
		for j := r.Intn(3); j > 0; j-- {
			rule.Args = append(rule.Args, args[r.Intn(len(args))])
		}
		p := guard.Policy{Mode: core.ModeAsk, ReadRoots: []string{`C:\repo\a`}, Allow: []guard.CommandRule{rule}, Enforcement: guard.EnforcementHook}
		if p.Validate() != nil {
			continue
		}
		base := strings.ToLower(rule.Program)
		if k := strings.IndexByte(base, '.'); k > 0 {
			base = base[:k]
		}
		for _, g := range refusedGroups {
			if contains(g, base) {
				t.Fatalf("accepted %+v", rule)
			}
		}
		if contains(interpreterNames, base) && (len(rule.Args) == 0 || rule.Tail != guard.TailNone) {
			t.Fatalf("accepted %+v", rule)
		}
		if contains(pkgTools, base) && (len(rule.Args) == 0 || contains(badSub, rule.Args[0])) {
			t.Fatalf("accepted %+v", rule)
		}
		got, err := guard.ParsePolicy(guard.FormatPolicy(p))
		if err != nil || len(got.Allow) != 1 {
			t.Fatalf("round trip %+v: %v", rule, err)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
