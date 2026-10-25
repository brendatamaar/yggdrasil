package guard

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

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
