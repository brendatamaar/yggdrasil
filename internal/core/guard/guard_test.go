package guard

import (
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The stubs must fail closed until G1 replaces them.
func TestStubsFailClosed(t *testing.T) {
	p := Policy{Mode: core.ModeCode, ReadRoots: []string{"/r"}, WriteRoots: []string{"/r"}, Enforcement: EnforcementHook}
	for _, c := range []Call{
		{Tool: ToolRead, Paths: []PathRef{{Canon: "/r/a", OK: true}}},
		{Tool: ToolWrite, Paths: []PathRef{{Canon: "/r/a", OK: true}}},
		{Tool: ToolExec, Argv: []string{"go", "test"}},
		{},
	} {
		if v := Decide(p, c); v.Allow || v.Reason == "" {
			t.Errorf("Decide(%+v) = %+v, want a denial with a reason", c, v)
		}
	}
	for _, m := range []core.TaskMode{core.ModeCode, core.ModeCurrentBranch, core.ModeProject} {
		if a := Admit(Request{Mode: m, OptIn: true, Trust: TrustTrusted, Enforcement: EnforcementHook}); a.Admitted || a.Refusal == "" {
			t.Errorf("Admit(%s) = %+v, want a refusal", m, a)
		}
	}
	if a := Admit(Request{Mode: core.ModeAsk, Trust: TrustUntrusted, Enforcement: EnforcementLimited}); !a.Admitted {
		t.Errorf("Admit(ask) = %+v, want admitted", a)
	}
	if _, err := ParseCommandLine("go test ./..."); err == nil {
		t.Error("ParseCommandLine stub accepted a line")
	}
	if _, err := ParsePolicy([]byte(`{}`)); err == nil {
		t.Error("ParsePolicy stub accepted a policy")
	}
	var s Integrity = Armed{Turn: "t", Before: Snapshot{Head: "a"}}
	if got, cmds := StepIntegrity(s, TurnEnds{Turn: "t"}); !reflect.DeepEqual(got, s) || cmds != nil {
		t.Errorf("StepIntegrity stub changed something")
	}
	var tr Trust = NoConfig{}
	if got, cmds := StepTrust(tr, UserTrusts{}); got != tr || cmds != nil {
		t.Errorf("StepTrust stub changed something")
	}
}

func TestEveryReasonHasAMessage(t *testing.T) {
	for r := range reasonMessages {
		if r.Message() == "" {
			t.Errorf("reason %q has no message", r)
		}
	}
	if Reason("nope").Message() == "" {
		t.Error("an unknown reason has an empty message")
	}
}

func TestPolicyValidate(t *testing.T) {
	ok := Policy{Mode: core.ModeCode, ReadRoots: []string{`C:\work\repo`}, WriteRoots: []string{`C:\work\repo`},
		Allow: []CommandRule{{Program: "go", Args: []string{"vet", "./..."}, Tail: TailNone}}, Enforcement: EnforcementHook}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	bad := map[string]func(p *Policy){
		"unknown mode":        func(p *Policy) { p.Mode = "x" },
		"no read root":        func(p *Policy) { p.ReadRoots = nil },
		"relative root":       func(p *Policy) { p.ReadRoots = []string{"repo"} },
		"root with ..":        func(p *Policy) { p.WriteRoots = []string{`C:\work\..\x`} },
		"write root in ask":   func(p *Policy) { p.Mode = core.ModeAsk },
		"program with a path": func(p *Policy) { p.Allow = []CommandRule{{Program: `bin\go`, Tail: TailNone}} },
		"empty program":       func(p *Policy) { p.Allow = []CommandRule{{Tail: TailNone}} },
		"unknown tail rule":   func(p *Policy) { p.Allow = []CommandRule{{Program: "go", Tail: "all"}} },
		"unknown enforcement": func(p *Policy) { p.Enforcement = "" },
	}
	for name, mutate := range bad {
		p := ok
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSnapshotEqualIgnoresPaths(t *testing.T) {
	a := Snapshot{Head: "h", Status: "s", DiffHash: "d", Paths: []string{"x"}}
	b := Snapshot{Head: "h", Status: "s", DiffHash: "d"}
	if !a.Equal(b) {
		t.Error("paths took part in Equal")
	}
	b.DiffHash = "e"
	if a.Equal(b) {
		t.Error("different diff hash compared equal")
	}
}
