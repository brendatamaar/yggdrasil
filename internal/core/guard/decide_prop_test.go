package guard

import (
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// G1
func TestPropWriteNeverOutsideRoots(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			p, c := genPolicy(r), genCall(r)
			c.Tool = ToolWrite
			if !Decide(p, c).Allow {
				continue
			}
			if len(c.Paths) == 0 {
				t.Fatalf("allowed a write naming no path: %+v", c)
			}
			for _, ref := range c.Paths {
				if !ref.OK || hasDotDot(ref.Canon) || !anyInside(p.WriteRoots, ref.Canon, p.FoldCase) {
					t.Fatalf("allowed write to %q with write roots %q (fold %v)", ref.Canon, p.WriteRoots, p.FoldCase)
				}
			}
		}
	})
}

// G2
func TestPropAskNeverWritesOrExecs(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			p, c := genPolicy(r), genCall(r)
			p.Mode = core.ModeAsk
			p.WriteRoots = nil
			if (c.Tool == ToolWrite || c.Tool == ToolExec) && Decide(p, c).Allow {
				t.Fatalf("Ask mode allowed %s: %+v", c.Tool, c)
			}
		}
	})
}

// G3
func TestPropExecOnlyAllowlisted(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			p, c := genPolicy(r), genCall(r)
			c.Tool = ToolExec
			c.Argv = genArgv(r)
			c.Paths = nil
			got := Decide(p, c).Allow
			if want := refExecAllowed(p, c); got != want {
				t.Fatalf("Decide=%v, reference=%v for %q (parse error %v, program %+v) with rules %+v", got, want, c.Argv, c.ParseErr, c.Program, p.Allow)
			}
		}
	})
}

// refExecAllowed is a reference for D9 to D12 written from the design table
// with regular expressions and no code shared with the implementation: the
// program is the bare name or the name plus ".exe", the resolved program is
// present and outside every write root, fixed arguments are exact, and every
// tail word is plain text (no escape, expansion or glob character, no leading
// "-", "@" or "~") and, when absolute, inside a read root.
func refExecAllowed(p Policy, c Call) bool {
	if p.Mode == core.ModeAsk || c.ParseErr || len(c.Argv) == 0 {
		return false
	}
	if c.Program == nil || !c.Program.OK {
		return false
	}
	if anyInside(p.WriteRoots, c.Program.Canon, p.FoldCase) {
		return false
	}
	for _, rule := range p.Allow {
		if refRuleHolds(rule, c.Argv, p) {
			return true
		}
	}
	return false
}

func refNameRe(name string, fold bool) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, ch := range []byte(name) {
		switch {
		case fold && ch >= 'a' && ch <= 'z':
			fmt.Fprintf(&b, "[%c%c]", ch, ch-32)
		case fold && ch >= 'A' && ch <= 'Z':
			fmt.Fprintf(&b, "[%c%c]", ch, ch+32)
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	ext := `\.exe`
	if fold {
		ext = `\.[Ee][Xx][Ee]`
	}
	b.WriteString("(" + ext + ")?$")
	return regexp.MustCompile(b.String())
}

var (
	refPlainWord = regexp.MustCompile(`^[A-Za-z0-9_%+=:./ -]+$`)
	refDriveAbs  = regexp.MustCompile(`^[A-Za-z]:/`)
)

func refRuleHolds(rule CommandRule, argv []string, p Policy) bool {
	if !refNameRe(rule.Program, p.FoldCase).MatchString(argv[0]) || len(argv)-1 < len(rule.Args) {
		return false
	}
	for i, a := range rule.Args {
		if argv[1+i] != a {
			return false
		}
	}
	tail := argv[1+len(rule.Args):]
	if rule.Tail == TailNone {
		return len(tail) == 0
	}
	if rule.Tail != TailPaths {
		return false
	}
	for _, a := range tail {
		if !refPlainWord.MatchString(a) || strings.ContainsAny(a[:1], "-@~") || hasDotDot(a) {
			return false
		}
		if strings.Contains(a, ":") && (!refDriveAbs.MatchString(a) || strings.Count(a, ":") != 1) {
			return false
		}
		if strings.HasPrefix(a, "/") || refDriveAbs.MatchString(a) {
			if !anyInside(p.ReadRoots, a, p.FoldCase) {
				return false
			}
		}
	}
	return true
}

// G4
func TestPropUnresolvedDenied(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			p, c := genPolicy(r), genCall(r)
			c.Paths = append(c.Paths, PathRef{Canon: p.ReadRoots[0] + `\ok.txt`, OK: true}, PathRef{Canon: p.ReadRoots[0] + `\bad.txt`, OK: false})
			if Decide(p, c).Allow {
				t.Fatalf("allowed a call with an unresolved path: %+v", c)
			}
		}
	})
}

// G5
func TestPropProtectedPathsDenied(t *testing.T) {
	names := []string{".git/config", `.git\HEAD`, ".GIT/x", ".claude/settings.json", ".CLAUDE/x", ".mcp.json", "CLAUDE.md",
		"sub/AGENTS.md", "opencode.json", ".opencode/a/b", "GEMINI.md", "deep/er/.git/hooks/pre-commit",
		".codex/config.toml", ".gemini/x", ".cursor/rules/a", "CLAUDE.local.md", "sub/claude.LOCAL.md", ".gitmodules", ".husky/pre-commit", ".githooks/x"}
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		p := genPolicy(r)
		p.WriteRoots = append(p.WriteRoots, p.ReadRoots...)
		root := p.WriteRoots[0]
		for _, n := range names {
			c := Call{Tool: ToolWrite, Paths: []PathRef{{Canon: root + `\` + n, OK: true}}}
			if Decide(p, c).Allow {
				t.Fatalf("mode %s allowed a write to %q", p.Mode, c.Paths[0].Canon)
			}
		}
	})
}

// G6
func TestPropPrefixSiblingOutside(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		root := pick(r, genRoots...)
		p := Policy{Mode: core.ModeCode, ReadRoots: []string{root}, WriteRoots: []string{root}, FoldCase: r.Intn(2) == 0, Enforcement: EnforcementHook}
		// Look-alikes: Kelvin sign for k, long s for s (EqualFold folds both).
		if lookalike := strings.NewReplacer("k", "K", "s", "ſ").Replace(root); lookalike != root && p.FoldCase {
			for _, tool := range []Tool{ToolRead, ToolSearch, ToolWrite} {
				if Decide(p, Call{Tool: tool, Paths: []PathRef{{Canon: lookalike + `\a.txt`, OK: true}}}).Allow {
					t.Fatalf("%s allowed look-alike %q of root %q", tool, lookalike, root)
				}
			}
		}
		for _, suffix := range []string{"-evil", "2", "x", "_old", ".bak"} {
			for _, tail := range []string{"", `\a.txt`, `/a/b.txt`} {
				canon := root + suffix + tail
				for _, tool := range []Tool{ToolRead, ToolSearch, ToolWrite} {
					if Decide(p, Call{Tool: tool, Paths: []PathRef{{Canon: canon, OK: true}}}).Allow {
						t.Fatalf("%s allowed sibling %q of root %q", tool, canon, root)
					}
				}
			}
		}
	})
}

// G7
func TestPropDecideIsPure(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 20; i++ {
			p, c := genPolicy(r), genCall(r)
			p0, c0 := clonePolicy(p), cloneCall(c)
			v1, v2 := Decide(p, c), Decide(p, c)
			if v1 != v2 {
				t.Fatalf("not deterministic: %+v vs %+v", v1, v2)
			}
			if !reflect.DeepEqual(p, p0) || !reflect.DeepEqual(c, c0) {
				t.Fatal("Decide changed its inputs")
			}
		}
	})
}

func clonePolicy(p Policy) Policy {
	q := p
	q.ReadRoots = append([]string(nil), p.ReadRoots...)
	q.WriteRoots = append([]string(nil), p.WriteRoots...)
	q.Allow = nil
	for _, a := range p.Allow {
		a.Args = append([]string(nil), a.Args...)
		q.Allow = append(q.Allow, a)
	}
	return q
}

func cloneCall(c Call) Call {
	d := c
	d.Paths = append([]PathRef(nil), c.Paths...)
	d.Globs = append([]string(nil), c.Globs...)
	d.Argv = append([]string(nil), c.Argv...)
	return d
}

// G8
func TestPropDenyMessageHoldsNoPath(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 30; i++ {
			p, c := genPolicy(r), genCall(r)
			for k := range c.Paths {
				c.Paths[k].Canon = "zqcanon" + c.Paths[k].Canon
				c.Paths[k].Raw = "zqraw" + c.Paths[k].Raw
			}
			c.ToolName = "zqtool"
			c.Argv = append(c.Argv, "zqarg")
			v := Decide(p, c)
			if v.Allow {
				continue
			}
			msg := v.Reason.Message()
			if msg == "" {
				t.Fatalf("deny %q has no message", v.Reason)
			}
			for _, secret := range []string{"zqcanon", "zqraw", "zqtool", "zqarg", `\`, "/"} {
				if strings.Contains(msg, secret) {
					t.Fatalf("message %q holds %q", msg, secret)
				}
			}
			for _, rt := range append(append([]string(nil), p.ReadRoots...), p.WriteRoots...) {
				if strings.Contains(msg, rt) {
					t.Fatalf("message %q holds root %q", msg, rt)
				}
			}
		}
	})
}

// G9
func TestPropSubagentIsolationDenied(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		p := genPolicy(r)
		iso := pick(r, "worktree", "x", " ", "WORKTREE")
		v := Decide(p, Call{Tool: ToolSubagent, Isolation: iso})
		if v.Allow || v.Reason != ReasonAgentWorktreeBlocked {
			t.Fatalf("isolation %q gave %+v", iso, v)
		}
		if !Decide(p, Call{Tool: ToolSubagent}).Allow {
			t.Fatal("a plain sub-agent was denied")
		}
	})
}
