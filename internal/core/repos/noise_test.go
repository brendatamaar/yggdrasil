package repos

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestNoiseDefaultsAreConservative(t *testing.T) {
	if NoiseMinUntracked != 100 || NoiseMinSample != 50 || NoiseMaxEntries != 32 || NoiseMaxBytes != 4<<10 {
		t.Error("the F0 noise thresholds changed; update docs/design/phase-6-noise.md first")
	}
	dirs := NoiseDirs()
	for _, bad := range []string{"src", "lib", "app", "*", ".git", "", "test", "docs"} {
		if slices.Contains(dirs, bad) {
			t.Errorf("%q must never be suggested", bad)
		}
	}
	for _, want := range []string{"node_modules", ".venv", "dist", "build", "target", "bin", "obj", "__pycache__"} {
		if !slices.Contains(dirs, want) {
			t.Errorf("the allowlist lacks %q", want)
		}
	}
}

func TestNoiseFactsValidate(t *testing.T) {
	if err := (NoiseFacts{Repo: "rep_1", UntrackedCount: 3, Sample: []string{"a"}}).Validate(); err != nil {
		t.Error(err)
	}
	if err := (NoiseFacts{Repo: "rep_1", UntrackedCount: 1, Sample: []string{"a", "b"}}).Validate(); err == nil {
		t.Error("a sample larger than the count was accepted")
	}
	if err := (NoiseFacts{Repo: "rep_1", UntrackedCount: 1, Sample: []string{"a", "b"}, Truncated: true}).Validate(); err != nil {
		t.Errorf("a truncated read may hold a lower-bound count: %v", err)
	}
	if err := (NoiseFacts{UntrackedCount: 1}).Validate(); err == nil {
		t.Error("facts without a repo were accepted")
	}
}

// Unsupported inputs are inert in NoSuggestion.
func TestNoiseUnhandledInputsAreInert(t *testing.T) {
	n := Noise{Repo: "rep_1", State: NoSuggestion{}}
	for _, in := range []NoiseInput{
		NoiseObserved{Facts: NoiseFacts{Repo: "rep_1", UntrackedCount: 500}}, NoiseSelected{Entries: []string{"/bin/"}},
		NoiseProposalDecided{Proposal: "prp_1"}, NoiseScopeRemoved{},
	} {
		got, cmds := NoiseStep(n, in)
		if _, ok := got.State.(NoSuggestion); !ok || len(cmds) != 0 {
			t.Errorf("NoiseStep(%T) = %+v, %v", in, got, cmds)
		}
	}
}

func noiseFacts(n int, dir string) NoiseFacts {
	f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100}
	for i := 0; i < n; i++ {
		f.Sample = append(f.Sample, fmt.Sprintf("%s/f%d", dir, i))
	}
	return f
}

func TestNoiseTransitionsAndEvidence(t *testing.T) {
	f := noiseFacts(50, "web/[app] #!/node_modules")
	n, cs := NoiseStep(Noise{Repo: f.Repo, State: NoSuggestion{}}, NoiseObserved{Facts: f})
	s, ok := n.State.(Suggested)
	if !ok || len(cs) != 0 || len(s.Candidates) != 1 {
		t.Fatalf("%+v %v", n, cs)
	}
	c := s.Candidates[0]
	if c.Pattern != `/web/\[app\]\ \#\!/node_modules/` || c.SampleCount != 50 || !c.Incomplete || c.Evidence.Total != 100 || c.Evidence.Sampled != 50 {
		t.Fatalf("%+v", c)
	}
	in := NoiseSelected{Entries: []string{c.Pattern}, Absent: true, RequestID: "req_1", Proposal: "prp_1", Now: time.Unix(1, 0)}
	waiting, cs := NoiseStep(n, in)
	if len(cs) != 1 {
		t.Fatalf("selection: %+v %v", waiting, cs)
	}
	in.Entries[0] = "mutated"
	if cs[0].(NoiseCreateIgnoreProposal).Entries[0] != c.Pattern {
		t.Fatal("command aliases input")
	}
	for _, event := range []NoiseInput{NoiseObserved{Facts: f}, NoiseSelected{}, NoiseProposalDecided{Proposal: "prp_2", Status: core.ProposalExecuted}, NoiseProposalDecided{Proposal: "prp_1", Status: core.ProposalConfirmed}} {
		got, cs := NoiseStep(waiting, event)
		if !reflect.DeepEqual(got, waiting) || len(cs) != 0 {
			t.Fatalf("late/unsupported event %T changed pending proposal", event)
		}
	}
	for _, status := range []core.ProposalStatus{core.ProposalRejected, core.ProposalExecuted, core.ProposalFailed} {
		got, cs := NoiseStep(waiting, NoiseProposalDecided{Proposal: "prp_1", Status: status})
		if _, ok := got.State.(NoSuggestion); !ok || len(cs) != 1 || cs[0].(NoiseRefreshStatus).Repo != "rep_1" {
			t.Fatal(got, cs)
		}
	}
	got, cs := NoiseStep(waiting, NoiseScopeRemoved{})
	if _, ok := got.State.(NoSuggestion); !ok || len(cs) != 0 {
		t.Fatal(got, cs)
	}
}

func TestNoiseSelectionRejectsEachRule(t *testing.T) {
	n, _ := NoiseStep(Noise{Repo: "rep_1", State: NoSuggestion{}}, NoiseObserved{Facts: noiseFacts(50, "dist")})
	valid := NoiseSelected{Entries: []string{"/dist/"}, Absent: true, RequestID: "req_1", Proposal: "prp_1", Now: time.Unix(1, 0)}
	for name, change := range map[string]func(*NoiseSelected){
		"empty": func(x *NoiseSelected) { x.Entries = nil }, "unknown": func(x *NoiseSelected) { x.Entries = []string{"/*"} },
		"duplicate": func(x *NoiseSelected) { x.Entries = []string{"/dist/", "/dist/"} }, "absent hash": func(x *NoiseSelected) { x.CurrentHash = strings.Repeat("a", 64) },
		"missing hash": func(x *NoiseSelected) { x.Absent = false }, "bad hash": func(x *NoiseSelected) { x.Absent = false; x.CurrentHash = strings.Repeat("z", 64) },
		"request": func(x *NoiseSelected) { x.RequestID = "bad space" }, "proposal": func(x *NoiseSelected) { x.Proposal = "" }, "time": func(x *NoiseSelected) { x.Now = time.Time{} },
		"too many": func(x *NoiseSelected) { x.Entries = make([]string, 33) },
	} {
		t.Run(name, func(t *testing.T) {
			x := valid
			change(&x)
			got, cs := NoiseStep(n, x)
			if !reflect.DeepEqual(got, n) || len(cs) != 0 {
				t.Fatal("accepted invalid selection")
			}
		})
	}
	valid.Absent = false
	valid.CurrentHash = strings.Repeat("a", 64)
	if _, cs := NoiseStep(n, valid); len(cs) != 1 {
		t.Fatal("existing empty file hash rejected")
	}
}

func TestNoisePathBoundary(t *testing.T) {
	for _, p := range []string{"dist/a", "日本語/dist/a", "a[?*] #!/dist/a"} {
		if !ValidNoisePath(p) {
			t.Errorf("rejected %q", p)
		}
	}
	for _, p := range []string{"", "/dist/a", "../dist/a", "a/./dist/a", "a//dist/a", `C:/dist/a`, `a\dist\a`, "a:stream/dist/a", "dist/a\n", "dist/a\r", "dist/a\t", "dist/a\x7f", "dist/a\u0085", ".git/dist/a", "a /dist/a", "a./dist/a", string([]byte{255}), strings.Repeat("x", 4097)} {
		if ValidNoisePath(p) {
			t.Errorf("accepted %q", p)
		}
	}
}

// Each seed is a sequence; remove-one shrinking reports a minimal failing
// sequence. Invariants are checked after every transition, including collisions.
func TestPropNoiseRequiresThreshold(t *testing.T)             { checkNoiseProperty(t, "threshold") }
func TestPropNoiseNeverSuggestsSourceOrWildcard(t *testing.T) { checkNoiseProperty(t, "allowlist") }
func TestPropTruncatedEvidenceNeverComplete(t *testing.T)     { checkNoiseProperty(t, "truncated") }
func TestPropNoiseObservationsHaveNoEffects(t *testing.T)     { checkNoiseProperty(t, "effects") }
func TestPropNoisePendingProposalIsImmutable(t *testing.T)    { checkNoiseProperty(t, "pending") }

func checkNoiseProperty(t *testing.T, invariant string) {
	t.Helper()
	check := func(inputs []NoiseInput) bool {
		n := Noise{Repo: "rep_1", State: NoSuggestion{}}
		for _, in := range inputs {
			before := n
			var cs []NoiseCommand
			n, cs = NoiseStep(n, in)
			if s, ok := n.State.(Suggested); ok {
				for i, c := range s.Candidates {
					if invariant == "threshold" && (c.Evidence.Total < 100 || c.SampleCount < 50 || c.SampleCount < (c.Evidence.Sampled+1)/2) {
						return false
					}
					if invariant == "allowlist" && (!slices.Contains(NoiseDirs(), c.Directory[strings.LastIndex(c.Directory, "/")+1:]) || !strings.HasPrefix(c.Pattern, "/") || !strings.HasSuffix(c.Pattern, "/")) {
						return false
					}
					if invariant == "allowlist" {
						for j := 0; j < len(c.Pattern); j++ {
							if c.Pattern[j] == '\\' {
								j++
								continue
							}
							if strings.ContainsRune("*?[]", rune(c.Pattern[j])) {
								return false
							}
						}
					}
					if invariant == "allowlist" && i > 0 && s.Candidates[i-1].Directory >= c.Directory {
						return false
					}
				}
				if x, ok := in.(NoiseObserved); ok && x.Facts.Truncated && invariant == "truncated" {
					for _, c := range s.Candidates {
						if !c.Incomplete {
							return false
						}
					}
				}
			}
			if _, ok := in.(NoiseObserved); ok && len(cs) != 0 && invariant == "effects" {
				return false
			}
			if _, ok := before.State.(AwaitingConfirmation); ok && invariant == "pending" {
				if _, observed := in.(NoiseObserved); observed && !reflect.DeepEqual(before, n) {
					return false
				}
			}
		}
		return true
	}
	for seed := int64(0); seed < 100; seed++ {
		r := rand.New(rand.NewSource(seed))
		var inputs []NoiseInput
		for i := 0; i < 40; i++ {
			f := noiseFacts(r.Intn(101), []string{"src", "dist", "a*/build", "node_modules/target"}[r.Intn(4)])
			f.UntrackedCount = r.Intn(200)
			f.Truncated = r.Intn(2) == 0
			switch r.Intn(5) {
			case 0, 1:
				inputs = append(inputs, NoiseObserved{Facts: f})
			case 2:
				inputs = append(inputs, NoiseScopeRemoved{})
			case 3:
				inputs = append(inputs, NoiseSelected{Entries: []string{"/dist/"}, Absent: true, RequestID: "req_1", Proposal: "prp_1", Now: time.Unix(1, 0)})
			case 4:
				inputs = append(inputs, NoiseProposalDecided{Proposal: "prp_1", Status: core.ProposalExecuted})
			}
		}
		if !check(inputs) {
			for i := 0; i < len(inputs); {
				short := append(append([]NoiseInput{}, inputs[:i]...), inputs[i+1:]...)
				if !check(short) {
					inputs = short
					i = 0
				} else {
					i++
				}
			}
			t.Fatalf("seed %d minimal inputs %+v", seed, inputs)
		}
	}
}

func FuzzNoisePath(f *testing.F) {
	for _, p := range []string{"dist/a", "a[?*]/build/file", "../dist/a", "C:/a", "a\n"} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p string) {
		if ValidNoisePath(p) {
			if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, ":") || !utf8.ValidString(p) {
				t.Fatal("accepted unsafe path")
			}
			for _, c := range p {
				if c < 32 || c == 127 {
					t.Fatal("control accepted")
				}
			}
		}
	})
}

func TestNoiseThresholdsAndDeterminism(t *testing.T) {
	for _, tc := range []struct {
		total, generated, other int
		want                    bool
	}{{99, 50, 0, false}, {100, 49, 0, false}, {100, 50, 50, true}, {101, 50, 51, false}, {101, 51, 50, true}} {
		f := noiseFacts(tc.generated, "dist")
		f.UntrackedCount = tc.total
		for i := 0; i < tc.other; i++ {
			f.Sample = append(f.Sample, fmt.Sprintf("src/s%d", i))
		}
		n, cs := NoiseStep(Noise{Repo: f.Repo, State: NoSuggestion{}}, NoiseObserved{Facts: f})
		_, ok := n.State.(Suggested)
		if ok != tc.want || len(cs) != 0 {
			t.Fatalf("%+v: %+v %v", tc, n, cs)
		}
	}
	f := noiseFacts(100, "node_modules/target")
	n, _ := NoiseStep(Noise{Repo: f.Repo, State: NoSuggestion{}}, NoiseObserved{Facts: f})
	slices.Reverse(f.Sample)
	got, _ := NoiseStep(Noise{Repo: f.Repo, State: NoSuggestion{}}, NoiseObserved{Facts: f})
	if !reflect.DeepEqual(n, got) {
		t.Fatal("sample order affects result")
	}
	f = noiseFacts(50, strings.Repeat("*", 3000)+"/dist")
	n, _ = NoiseStep(Noise{Repo: f.Repo, State: NoSuggestion{}}, NoiseObserved{Facts: f})
	s := n.State.(Suggested)
	_, cs := NoiseStep(n, NoiseSelected{Entries: []string{s.Candidates[0].Pattern}, Absent: true, RequestID: "req_1", Proposal: "prp_1", Now: time.Unix(1, 0)})
	if len(cs) != 0 {
		t.Fatal("oversized escaped selection accepted")
	}
}
