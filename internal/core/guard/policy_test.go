package guard

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func genValidPolicy(r *rand.Rand) Policy {
	p := genPolicy(r)
	if p.Mode == core.ModeAsk {
		p.WriteRoots = nil
	}
	return p
}

func TestPolicyRoundTrip(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		p := genValidPolicy(r)
		if err := p.Validate(); err != nil {
			t.Fatalf("generator made an invalid policy: %v", err)
		}
		got, err := ParsePolicy(FormatPolicy(p))
		if err != nil {
			t.Fatalf("parse(format(p)): %v\n%s", err, FormatPolicy(p))
		}
		if !reflect.DeepEqual(got, p) {
			t.Fatalf("round trip changed the policy:\n got %#v\nwant %#v", got, p)
		}
	})
}

func TestParsePolicyAcceptedImpliesValid(t *testing.T) {
	good := `{"mode":"code","readRoots":["C:\\work\\repo"],"writeRoots":["C:\\work\\repo"],"allow":[{"program":"go","args":["vet","./..."],"tail":"none"}],"foldCase":true,"enforcement":"hook"}`
	for _, in := range []string{good, strings.Replace(good, `"foldCase":true,`, "", 1), ` ` + good + "\n"} {
		p, err := ParsePolicy([]byte(in))
		if err != nil {
			t.Fatalf("rejected %s: %v", in, err)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("accepted but invalid: %v", err)
		}
	}
}

// One test per rejection rule of the Boundary table, plus the JSON-level ones.
func TestParsePolicyRejectsEachRule(t *testing.T) {
	base := func(edit func(string) string) []byte {
		return []byte(edit(`{"mode":"code","readRoots":["C:\\work\\repo"],"writeRoots":["C:\\work\\repo"],"allow":[{"program":"go","args":["test"],"tail":"none"}],"foldCase":true,"enforcement":"hook"}`))
	}
	rep := func(old, new string) func(string) string {
		return func(s string) string {
			if !strings.Contains(s, old) {
				panic("test bug: " + old)
			}
			return strings.Replace(s, old, new, 1)
		}
	}
	rules := map[string][]byte{
		"unknown mode":             base(rep(`"mode":"code"`, `"mode":"yolo"`)),
		"empty mode":               base(rep(`"mode":"code"`, `"mode":""`)),
		"relative read root":       base(rep(`"readRoots":["C:\\work\\repo"]`, `"readRoots":["repo"]`)),
		"empty read root":          base(rep(`"readRoots":["C:\\work\\repo"]`, `"readRoots":[""]`)),
		"relative write root":      base(rep(`"writeRoots":["C:\\work\\repo"]`, `"writeRoots":["x\\y"]`)),
		"read root with ..":        base(rep(`"readRoots":["C:\\work\\repo"]`, `"readRoots":["C:\\work\\..\\x"]`)),
		"write root with ..":       base(rep(`"writeRoots":["C:\\work\\repo"]`, `"writeRoots":["/a/../b"]`)),
		"no read roots":            base(rep(`"readRoots":["C:\\work\\repo"]`, `"readRoots":[]`)),
		"read roots missing":       base(rep(`"readRoots":["C:\\work\\repo"],`, ``)),
		"write roots in ask":       base(rep(`"mode":"code"`, `"mode":"ask"`)),
		"empty program":            base(rep(`"program":"go"`, `"program":""`)),
		"program with separator":   base(rep(`"program":"go"`, `"program":"bin\\go"`)),
		"program with slash":       base(rep(`"program":"go"`, `"program":"bin/go"`)),
		"unknown tail rule":        base(rep(`"tail":"none"`, `"tail":"all"`)),
		"missing tail rule":        base(rep(`,"tail":"none"`, ``)),
		"unknown enforcement":      base(rep(`"enforcement":"hook"`, `"enforcement":"yolo"`)),
		"unknown field":            base(rep(`"foldCase":true`, `"foldCase":true,"extra":1`)),
		"wrong type":               base(rep(`"foldCase":true`, `"foldCase":"yes"`)),
		"trailing data":            base(func(s string) string { return s + `{}` }),
		"not JSON":                 []byte(`mode=code`),
		"empty input":              nil,
		"null":                     []byte(`null`),
		"array":                    []byte(`[]`),
		"empty object":             []byte(`{}`),
		"truncated":                base(func(s string) string { return s[:len(s)/2] }),
		"NUL in root":              base(rep(`"readRoots":["C:\\work\\repo"]`, `"readRoots":["C:\\work\\repo\u0000"]`)),
		"root that is not absolut": base(rep(`"readRoots":["C:\\work\\repo"]`, `"readRoots":["C:work"]`)),
	}
	for name, data := range rules {
		t.Run(name, func(t *testing.T) {
			if p, err := ParsePolicy(data); err == nil {
				t.Fatalf("accepted: %#v", p)
			}
		})
	}
}

func FuzzParsePolicy(f *testing.F) {
	f.Add(FormatPolicy(Policy{Mode: core.ModeCode, ReadRoots: []string{`C:\r`}, WriteRoots: []string{`C:\r`}, Enforcement: EnforcementHook,
		Allow: []CommandRule{{Program: "go", Args: []string{"test"}, Tail: TailPaths}}}))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"mode":"ask","readRoots":["/r"],"enforcement":"limited"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ParsePolicy(data)
		if err != nil {
			return
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("accepted an invalid policy: %v", err)
		}
		back, err := ParsePolicy(FormatPolicy(p))
		if err != nil || !reflect.DeepEqual(back, p) {
			t.Fatalf("accepted policy does not round-trip: %v\n%#v\n%#v", err, back, p)
		}
	})
}
