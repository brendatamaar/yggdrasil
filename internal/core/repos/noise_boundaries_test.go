package repos

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// files returns n distinct sample paths under dir.
func files(dir string, n int, prefix string) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%s/%s%d", dir, prefix, i))
	}
	return out
}

func analyse(t *testing.T, f NoiseFacts) (Noise, NoiseState) {
	t.Helper()
	n, cs := NoiseStep(Noise{Repo: f.Repo, State: NoSuggestion{}}, NoiseObserved{Facts: f})
	if len(cs) != 0 {
		t.Fatalf("observation produced commands: %v", cs)
	}
	return n, n.State
}

// Exact threshold boundaries, written as literals from the design: total >= 100,
// >= 50 sampled files under one directory, and at least half the sample there.
func TestNoiseThresholdBoundariesExact(t *testing.T) {
	for _, tc := range []struct {
		name          string
		total, inDist int
		others        int
		want          bool
	}{
		{"total 99 is below", 99, 99, 0, false},
		{"total 100 is enough", 100, 100, 0, true},
		{"49 under the directory", 100, 49, 51, false},
		{"50 under the directory, sample 50", 100, 50, 0, true},
		{"50 of an even sample of 100 is exactly half", 100, 50, 50, true},
		{"50 of an odd sample of 101 is below half", 101, 50, 51, false},
		{"51 of an odd sample of 101 is above half", 101, 51, 50, true},
		{"50 of an odd sample of 99 is above half", 100, 50, 49, true},
		{"50 of 102 is below half (needs 51)", 102, 50, 52, false},
		{"51 of 102 is exactly half", 102, 51, 51, true},
		{"sample of 49 is too small even when all of it is there", 150, 49, 0, false},
		{"sample of 50 with a high total", 5000, 50, 0, true},
		{"empty", 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NoiseFacts{Repo: "rep_1", UntrackedCount: tc.total}
			f.Sample = append(files("dist", tc.inDist, "f"), files("src", tc.others, "s")...)
			_, st := analyse(t, f)
			s, got := st.(Suggested)
			if got != tc.want {
				t.Fatalf("suggested = %v, want %v (%+v)", got, tc.want, st)
			}
			if got && (len(s.Candidates) != 1 || s.Candidates[0].SampleCount != tc.inDist || s.Candidates[0].Evidence.UnderDirectory != tc.inDist) {
				t.Fatalf("candidates %+v", s.Candidates)
			}
		})
	}
}

// Two disjoint directories can both be exactly half of an even sample: each is
// offered once, sorted by directory, with its own evidence.
func TestNoiseTwoHalvesAreBothOffered(t *testing.T) {
	f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: append(files("dist", 50, "a"), files("build", 50, "b")...)}
	_, st := analyse(t, f)
	s := st.(Suggested)
	if len(s.Candidates) != 2 || s.Candidates[0].Directory != "build" || s.Candidates[1].Directory != "dist" {
		t.Fatalf("%+v", s.Candidates)
	}
	for _, c := range s.Candidates {
		if c.SampleCount != 50 || c.Evidence != (NoiseEvidence{Total: 100, Sampled: 100, UnderDirectory: 50}) || c.Incomplete {
			t.Fatalf("%+v", c)
		}
	}
}

// A file under nested generated directories counts once for each exact
// directory on its path, never twice for the same one.
func TestNoiseNestedGeneratedDirectoriesCountOncePerExactDirectory(t *testing.T) {
	f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100}
	f.Sample = append(files("node_modules", 25, "a"), files("node_modules/dist", 25, "b")...)
	f.Sample = append(f.Sample, files("src", 50, "s")...)
	_, st := analyse(t, f)
	// node_modules holds 25 direct + 25 nested = 50 of 100; dist holds 25 only.
	s, ok := st.(Suggested)
	if !ok || len(s.Candidates) != 1 || s.Candidates[0].Directory != "node_modules" || s.Candidates[0].SampleCount != 50 {
		t.Fatalf("%+v", st)
	}
	// The same directory name twice in one path counts one file once per exact
	// directory: "dist" and "dist/dist" each hold 50 files; the inner is nested.
	g := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: files("dist/dist", 50, "f")}
	_, st = analyse(t, g)
	s = st.(Suggested)
	if len(s.Candidates) != 1 || s.Candidates[0].Directory != "dist" || s.Candidates[0].SampleCount != 50 {
		t.Fatalf("%+v", s.Candidates)
	}
}

// The allowlist is exact: it does not match by case, by suffix or by prefix, and
// a file whose own name is on the list is not a directory.
func TestNoiseNeverSuggestsOutsideTheAllowlist(t *testing.T) {
	for _, dir := range []string{"src", "Dist", "NODE_MODULES", "node_modules2", "my_dist", "dist.old", "dist ", "vendor", "lib", "a/b", ".git", "x/.venv2", "*", "build*"} {
		f := NoiseFacts{Repo: "rep_1", UntrackedCount: 200, Sample: files(dir, 100, "f")}
		if f.Validate() != nil {
			continue // not representable; rejected at the boundary
		}
		if _, st := analyse(t, f); st != (NoSuggestion{}) {
			t.Errorf("directory %q was suggested: %+v", dir, st)
		}
	}
	// A file named like a generated directory is not a directory.
	f := NoiseFacts{Repo: "rep_1", UntrackedCount: 200}
	for i := 0; i < 100; i++ {
		f.Sample = append(f.Sample, fmt.Sprintf("d%d/dist", i))
	}
	if _, st := analyse(t, f); st != (NoSuggestion{}) {
		t.Errorf("files named dist were suggested: %+v", st)
	}
}

// Candidate counts come only from the sample: raising the reader's total changes
// the labelled total and completeness, never the per-directory evidence.
func TestNoiseEvidenceTotalSampleAndDirectoryAreNotInterchanged(t *testing.T) {
	sample := append(files("web/node_modules", 70, "a"), files("src", 30, "s")...)
	base := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: sample}
	_, st := analyse(t, base)
	want := st.(Suggested).Candidates[0]
	for _, total := range []int{100, 101, 5000, 123456} {
		for _, trunc := range []bool{false, true} {
			f := base
			f.UntrackedCount, f.Truncated = total, trunc
			_, st := analyse(t, f)
			c := st.(Suggested).Candidates[0]
			wantIncomplete := trunc || total > 100
			if c.SampleCount != 70 || c.Evidence.UnderDirectory != 70 || c.Evidence.Sampled != 100 || c.Evidence.Total != total || c.Incomplete != wantIncomplete || c.Pattern != want.Pattern {
				t.Errorf("total=%d truncated=%v: %+v", total, trunc, c)
			}
			if sg := st.(Suggested); sg.Evidence.Total != total || sg.Evidence.Sampled != 100 {
				t.Errorf("state evidence %+v", sg.Evidence)
			}
		}
	}
	// A count that is only a lower bound (truncated, count below the sample size)
	// is accepted and stays labelled incomplete.
	f := base
	f.UntrackedCount, f.Truncated = 100, true
	f.Sample = append(slices.Clone(sample), files("src", 10, "t")...)
	if f.Validate() != nil {
		t.Fatal("a truncated lower-bound count must be representable")
	}
	_, st = analyse(t, f)
	if c := st.(Suggested).Candidates[0]; !c.Incomplete || c.Evidence.Sampled != 110 || c.Evidence.Total != 100 {
		t.Fatalf("%+v", c)
	}
}

// The order of the sample, and which spelling of the same set arrives, never
// changes candidates, evidence or patterns.
func TestPropNoiseResultIsOrderIndependent(t *testing.T) {
	dirs := []string{"node_modules", "web/node_modules", "a b/build", "[x]/target", "日本語/bin", "dist", "src", "bin/obj"}
	suggested := 0
	for seed := int64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewSource(seed))
		f := NoiseFacts{Repo: "rep_1"}
		for i, n := 0, 40+r.Intn(160); i < n; i++ {
			d := dirs[r.Intn(len(dirs))]
			if r.Intn(2) == 0 {
				d = dirs[0]
			}
			f.Sample = append(f.Sample, fmt.Sprintf("%s/f%d", d, i))
		}
		f.UntrackedCount = len(f.Sample) + r.Intn(3)
		f.Truncated = r.Intn(2) == 0
		first, _ := NoiseStep(Noise{Repo: "rep_1", State: NoSuggestion{}}, NoiseObserved{Facts: f})
		if _, ok := first.State.(Suggested); ok {
			suggested++
		}
		for k := 0; k < 3; k++ {
			g := f
			g.Sample = slices.Clone(f.Sample)
			r.Shuffle(len(g.Sample), func(i, j int) { g.Sample[i], g.Sample[j] = g.Sample[j], g.Sample[i] })
			again, _ := NoiseStep(Noise{Repo: "rep_1", State: NoSuggestion{}}, NoiseObserved{Facts: g})
			if !reflect.DeepEqual(first, again) {
				t.Fatalf("seed %d: a permutation of the sample changed the result:\n%+v\n%+v", seed, first, again)
			}
		}
	}
	if suggested < 20 {
		t.Fatalf("only %d of 200 samples suggested anything; the property is vacuous", suggested)
	}
}

// Metacharacters, spaces and Unicode are escaped or kept literal; the pattern is
// anchored and has no unescaped wildcard.
func TestNoisePatternEscapingIsLiteral(t *testing.T) {
	for _, tc := range []struct{ dir, pattern string }{
		{"dist", "/dist/"},
		{"web/node_modules", "/web/node_modules/"},
		{"a b/build", `/a\ b/build/`},
		{"[x]/target", `/\[x\]/target/`},
		{"w*?/__pycache__", `/w\*\?/__pycache__/`},
		// names Windows cannot create: the escaping is still checked here, as text
		{"a*b/dist", `/a\*b/dist/`},
		{"q?/obj", `/q\?/obj/`},
		{`a"b/dist`, `/a"b/dist/`},
		{"a&b|c/dist", "/a&b|c/dist/"},
		{"#!/dist", `/\#\!/dist/`},
		{"日本語/bin", "/日本語/bin/"},
		{"é/ü ö/obj", `/é/ü\ ö/obj/`},
		{"-x/dist", "/-x/dist/"},
		{"a{b,c}/dist", "/a{b,c}/dist/"},
		{"$HOME/dist", "/$HOME/dist/"},
		{"~/dist", "/~/dist/"},
		{`C-/dist`, "/C-/dist/"},
	} {
		f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: files(tc.dir, 60, "f")}
		_, st := analyse(t, f)
		s, ok := st.(Suggested)
		if !ok || s.Candidates[0].Pattern != tc.pattern || s.Candidates[0].Directory != tc.dir {
			t.Errorf("dir %q: %+v, want pattern %q", tc.dir, st, tc.pattern)
		}
	}
}

// Windows-style input never turns into a candidate: drive letters, backslashes,
// ADS names, CRLF and trailing dot/space names are rejected, and the whole
// observation is inert, so they can neither hide nor create a suggestion.
func TestNoiseWindowsStyleInputIsInert(t *testing.T) {
	good := files("web/node_modules", 60, "f")
	for _, bad := range []string{
		`C:/web/node_modules/x`, `C:\web\node_modules\x`, `web\node_modules\x`, `web/node_modules/x:stream`,
		"web/node_modules/x\r\n", "web/node_modules/x\r", "web/node_modules./x", "web/node_modules /x",
		`\\server\share\node_modules\x`, "//server/share/node_modules/x", "web/NUL\x00/x",
	} {
		f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: append(slices.Clone(good), bad)}
		n, cs := NoiseStep(Noise{Repo: "rep_1", State: NoSuggestion{}}, NoiseObserved{Facts: f})
		if n.State != (NoSuggestion{}) || len(cs) != 0 {
			t.Errorf("%q: %+v", bad, n)
		}
		// An earlier suggestion survives an invalid observation untouched.
		first, _ := NoiseStep(Noise{Repo: "rep_1", State: NoSuggestion{}}, NoiseObserved{Facts: NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: good}})
		again, cs := NoiseStep(first, NoiseObserved{Facts: f})
		if !reflect.DeepEqual(first, again) || len(cs) != 0 {
			t.Errorf("%q: an invalid observation replaced the current suggestion", bad)
		}
	}
	// Names with spaces, unicode and a long path are fine.
	long := strings.Repeat("d", 200)
	for _, dir := range []string{"Program Files/node_modules", "プロジェクト/dist", long + "/" + long + "/target"} {
		f := NoiseFacts{Repo: "rep_1", UntrackedCount: 60, Sample: files(dir, 60, "f")}
		f.UntrackedCount = 100
		if _, st := analyse(t, f); st == (NoSuggestion{}) {
			t.Errorf("%q: valid Windows-style path produced no suggestion", dir)
		}
	}
}

func suggestedWith(t *testing.T, f NoiseFacts) (Noise, Suggested) {
	t.Helper()
	n, st := analyse(t, f)
	return n, st.(Suggested)
}

// longDir returns a directory path of exactly n bytes ending in "/dist", built
// from segments of at most 100 bytes so that it is also a plausible Windows path.
func longDir(n int) string {
	pre := make([]byte, n-len("/dist"))
	for i := range pre {
		pre[i] = 'x'
		if (i+1)%101 == 0 {
			pre[i] = '/'
		}
	}
	if pre[len(pre)-1] == '/' {
		pre[len(pre)-1] = 'x'
	}
	return string(pre) + "/dist"
}

// Selection bounds: 4 KiB counts each entry plus a separator; the boundary is
// exact, in both directions.
func TestNoiseSelectionByteBoundaryIsExact(t *testing.T) {
	const names = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// One candidate: the pattern is "/"+dir+"/", so the selection is len(dir)+3.
	for _, tc := range []struct {
		dirLen int
		want   bool
	}{{4093, true}, {4094, false}} {
		dir := longDir(tc.dirLen)
		f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100}
		for i := 0; i < 50; i++ {
			f.Sample = append(f.Sample, dir+"/"+names[i:i+1])
		}
		if err := f.Validate(); err != nil {
			t.Fatalf("setup: %v", err)
		}
		n, s := suggestedWith(t, f)
		pat := s.Candidates[0].Pattern
		if len(pat)+1 != tc.dirLen+3 {
			t.Fatalf("setup: selection size %d", len(pat)+1)
		}
		_, cs := NoiseStep(n, NoiseSelected{Entries: []string{pat}, Absent: true, RequestID: "req_1", Proposal: "prp_1"})
		if (len(cs) == 1) != tc.want {
			t.Errorf("selection of %d bytes: accepted=%v, want %v", len(pat)+1, len(cs) == 1, tc.want)
		}
	}
	// Two candidates: each fine alone, together over the limit by one byte.
	half := func(dirLen int, tag string) []string {
		dir := tag + longDir(dirLen)[1:]
		var out []string
		for i := 0; i < 50; i++ {
			out = append(out, fmt.Sprintf("%s/f%d", dir, i))
		}
		return out
	}
	for _, tc := range []struct {
		a, b int
		want bool
	}{{2045, 2045, true}, {2046, 2045, false}} {
		f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: append(half(tc.a, "p"), half(tc.b, "q")...)}
		if err := f.Validate(); err != nil {
			t.Fatalf("setup: %v", err)
		}
		n, s := suggestedWith(t, f)
		if len(s.Candidates) != 2 {
			t.Fatalf("setup: %+v", s.Candidates)
		}
		entries := []string{s.Candidates[0].Pattern, s.Candidates[1].Pattern}
		_, one := NoiseStep(n, NoiseSelected{Entries: entries[:1], Absent: true, RequestID: "req_1", Proposal: "prp_1"})
		_, both := NoiseStep(n, NoiseSelected{Entries: entries, Absent: true, RequestID: "req_1", Proposal: "prp_1"})
		if len(one) != 1 || (len(both) == 1) != tc.want {
			t.Errorf("sizes %d+%d: single accepted=%v, pair accepted=%v, want pair %v (total %d)", tc.a, tc.b, len(one) == 1, len(both) == 1, tc.want, len(entries[0])+len(entries[1])+2)
		}
	}
}

// Every precondition of a selection has its own refusal, and an accepted
// selection carries exactly the caller's precondition, in a stable order.
func TestNoiseSelectionPreconditionsAndStableCommand(t *testing.T) {
	f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: append(files("dist", 50, "a"), files("build", 50, "b")...)}
	n, s := suggestedWith(t, f)
	dist, build := s.Candidates[1].Pattern, s.Candidates[0].Pattern
	ok := NoiseSelected{Entries: []string{dist, build}, Absent: true, RequestID: "req_1", Proposal: "prp_1"}
	refuse := map[string]func(*NoiseSelected){
		"absent with a hash":            func(x *NoiseSelected) { x.CurrentHash = strings.Repeat("a", 64) },
		"present without a hash":        func(x *NoiseSelected) { x.Absent = false },
		"uppercase hash":                func(x *NoiseSelected) { x.Absent, x.CurrentHash = false, strings.Repeat("A", 64) },
		"short hash":                    func(x *NoiseSelected) { x.Absent, x.CurrentHash = false, strings.Repeat("a", 63) },
		"long hash":                     func(x *NoiseSelected) { x.Absent, x.CurrentHash = false, strings.Repeat("a", 65) },
		"non-hex hash":                  func(x *NoiseSelected) { x.Absent, x.CurrentHash = false, strings.Repeat("g", 64) },
		"hash with a separator":         func(x *NoiseSelected) { x.Absent, x.CurrentHash = false, strings.Repeat("a", 63)+"\n" },
		"unknown entry":                 func(x *NoiseSelected) { x.Entries = []string{dist, "/src/"} },
		"candidate without its slash":   func(x *NoiseSelected) { x.Entries = []string{strings.TrimSuffix(dist, "/")} },
		"candidate in another case":     func(x *NoiseSelected) { x.Entries = []string{strings.ToUpper(dist)} },
		"candidate with padding":        func(x *NoiseSelected) { x.Entries = []string{" " + dist} },
		"candidate as a directory name": func(x *NoiseSelected) { x.Entries = []string{"dist"} },
		"duplicate entry":               func(x *NoiseSelected) { x.Entries = []string{dist, dist} },
		"empty selection":               func(x *NoiseSelected) { x.Entries = []string{} },
		"empty entry":                   func(x *NoiseSelected) { x.Entries = []string{dist, ""} },
		"blank proposal id":             func(x *NoiseSelected) { x.Proposal = "" },
		"proposal id with a space":      func(x *NoiseSelected) { x.Proposal = "prp 1" },
		"blank request id":              func(x *NoiseSelected) { x.RequestID = "" },
		"request id with a slash":       func(x *NoiseSelected) { x.RequestID = "req/1" },
	}
	for name, change := range refuse {
		t.Run(name, func(t *testing.T) {
			x := ok
			x.Entries = slices.Clone(ok.Entries)
			change(&x)
			got, cs := NoiseStep(n, x)
			if !reflect.DeepEqual(got, n) || len(cs) != 0 {
				t.Fatalf("accepted: %+v %v", got, cs)
			}
		})
	}
	// Absent and an existing empty file are different preconditions.
	emptyHash := genEmptyHash
	_, absent := NoiseStep(n, ok)
	present := ok
	present.Absent, present.CurrentHash = false, emptyHash
	_, empty := NoiseStep(n, present)
	a, e := absent[0].(NoiseCreateIgnoreProposal), empty[0].(NoiseCreateIgnoreProposal)
	if !a.ExpectAbsent || a.ExpectHash != "" || e.ExpectAbsent || e.ExpectHash != emptyHash {
		t.Fatalf("absent %+v, empty %+v", a, e)
	}
	// A subset is a valid selection, and the entry order is canonical.
	_, one := NoiseStep(n, NoiseSelected{Entries: []string{build}, Absent: true, RequestID: "req_1", Proposal: "prp_1"})
	if len(one) != 1 || !reflect.DeepEqual(one[0].(NoiseCreateIgnoreProposal).Entries, []string{build}) {
		t.Fatalf("subset: %v", one)
	}
	rev := ok
	rev.Entries = []string{build, dist}
	_, c1 := NoiseStep(n, ok)
	_, c2 := NoiseStep(n, rev)
	if !reflect.DeepEqual(c1, c2) || !slices.IsSorted(c1[0].(NoiseCreateIgnoreProposal).Entries) {
		t.Fatalf("entry order changed the command: %v vs %v", c1, c2)
	}
}

// A pending proposal is immutable. Re-observation, a second selection and a
// decision for another proposal leave it alone; its own terminal decision clears
// it exactly once; a non-terminal status is ignored.
func TestNoisePendingProposalLifecycle(t *testing.T) {
	f := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: files("dist", 60, "f")}
	n, s := suggestedWith(t, f)
	pat := s.Candidates[0].Pattern
	waiting, cs := NoiseStep(n, NoiseSelected{Entries: []string{pat}, Absent: true, RequestID: "req_1", Proposal: "prp_1"})
	if len(cs) != 1 {
		t.Fatal(cs)
	}
	other := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: files("build", 60, "f")}
	for name, in := range map[string]NoiseInput{
		"new observation":     NoiseObserved{Facts: other},
		"second selection":    NoiseSelected{Entries: []string{pat}, Absent: true, RequestID: "req_2", Proposal: "prp_2"},
		"other proposal":      NoiseProposalDecided{Proposal: "prp_2", Status: core.ProposalExecuted},
		"pending status":      NoiseProposalDecided{Proposal: "prp_1", Status: core.ProposalPending},
		"confirmed (running)": NoiseProposalDecided{Proposal: "prp_1", Status: core.ProposalConfirmed},
		"unknown status":      NoiseProposalDecided{Proposal: "prp_1", Status: "bogus"},
	} {
		got, cs := NoiseStep(waiting, in)
		if !reflect.DeepEqual(got, waiting) || len(cs) != 0 {
			t.Errorf("%s changed the pending proposal: %+v %v", name, got, cs)
		}
	}
	done, cs := NoiseStep(waiting, NoiseProposalDecided{Proposal: "prp_1", Status: core.ProposalExecuted})
	if done.State != (NoSuggestion{}) || len(cs) != 1 || cs[0] != (NoiseRefreshStatus{Repo: "rep_1"}) {
		t.Fatalf("%+v %v", done, cs)
	}
	again, cs := NoiseStep(done, NoiseProposalDecided{Proposal: "prp_1", Status: core.ProposalExecuted})
	if !reflect.DeepEqual(again, done) || len(cs) != 0 {
		t.Fatalf("a repeated decision refreshed twice: %v", cs)
	}
	// After the refresh arrives as a new observation, a new proposal may follow.
	n2, _ := NoiseStep(done, NoiseObserved{Facts: other})
	if _, ok := n2.State.(Suggested); !ok {
		t.Fatalf("%+v", n2)
	}
	// Scope removal clears a pending proposal without any command.
	gone, cs := NoiseStep(waiting, NoiseScopeRemoved{})
	if gone.State != (NoSuggestion{}) || len(cs) != 0 {
		t.Fatalf("%+v %v", gone, cs)
	}
	// A late decision after scope removal is inert.
	late, cs := NoiseStep(gone, NoiseProposalDecided{Proposal: "prp_1", Status: core.ProposalExecuted})
	if !reflect.DeepEqual(late, gone) || len(cs) != 0 {
		t.Fatalf("%+v %v", late, cs)
	}
}

// A stale candidate cannot be selected: after a re-observation replaces the
// candidate list, an entry from the old list is not a current candidate.
func TestNoiseStaleCandidateIsRefused(t *testing.T) {
	first := NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: files("dist", 60, "f")}
	n, s := suggestedWith(t, first)
	old := s.Candidates[0].Pattern
	n, _ = NoiseStep(n, NoiseObserved{Facts: NoiseFacts{Repo: "rep_1", UntrackedCount: 100, Sample: files("build", 60, "f")}})
	got, cs := NoiseStep(n, NoiseSelected{Entries: []string{old}, Absent: true, RequestID: "req_1", Proposal: "prp_1"})
	if !reflect.DeepEqual(got, n) || len(cs) != 0 {
		t.Fatalf("stale selection accepted: %+v %v", got, cs)
	}
	// And after the evidence drops below the threshold nothing can be selected.
	n, _ = NoiseStep(n, NoiseObserved{Facts: NoiseFacts{Repo: "rep_1", UntrackedCount: 3, Sample: []string{"a"}}})
	if n.State != (NoSuggestion{}) {
		t.Fatalf("%+v", n)
	}
}

// Facts of another repo never change this repo's state, even if valid.
func TestNoiseObservationOfAnotherRepoIsInert(t *testing.T) {
	n := Noise{Repo: "rep_1", State: NoSuggestion{}}
	f := NoiseFacts{Repo: "rep_2", UntrackedCount: 100, Sample: files("dist", 60, "f")}
	if got, cs := NoiseStep(n, NoiseObserved{Facts: f}); !reflect.DeepEqual(got, n) || len(cs) != 0 {
		t.Fatalf("%+v %v", got, cs)
	}
}

// NoiseFacts.Validate has one refusal per rule.
func TestNoiseFactsValidateRejectsEachRule(t *testing.T) {
	ok := NoiseFacts{Repo: "rep_1", UntrackedCount: 3, Sample: []string{"dist/a", "dist/b"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	many := make([]string, 5001)
	for i := range many {
		many[i] = fmt.Sprintf("dist/f%d", i)
	}
	for name, change := range map[string]func(*NoiseFacts){
		"no repo":                       func(f *NoiseFacts) { f.Repo = "" },
		"repo with whitespace":          func(f *NoiseFacts) { f.Repo = "rep 1" },
		"negative count":                func(f *NoiseFacts) { f.UntrackedCount = -1 },
		"sample above count, complete":  func(f *NoiseFacts) { f.UntrackedCount = 1 },
		"5001 paths, truncated":         func(f *NoiseFacts) { f.Sample, f.UntrackedCount, f.Truncated = many, 9000, true },
		"duplicate path":                func(f *NoiseFacts) { f.Sample = []string{"dist/a", "dist/a"} },
		"empty path":                    func(f *NoiseFacts) { f.Sample = []string{""} },
		"absolute path":                 func(f *NoiseFacts) { f.Sample = []string{"/dist/a"} },
		"drive path":                    func(f *NoiseFacts) { f.Sample = []string{"C:/dist/a"} },
		"backslash path":                func(f *NoiseFacts) { f.Sample = []string{`dist\a`} },
		"alternate data stream":         func(f *NoiseFacts) { f.Sample = []string{"dist/a:s"} },
		"traversal":                     func(f *NoiseFacts) { f.Sample = []string{"dist/../a"} },
		"dot component":                 func(f *NoiseFacts) { f.Sample = []string{"dist/./a"} },
		"empty component":               func(f *NoiseFacts) { f.Sample = []string{"dist//a"} },
		"git component":                 func(f *NoiseFacts) { f.Sample = []string{"x/.git/a"} },
		"git component, other case":     func(f *NoiseFacts) { f.Sample = []string{"x/.GiT/a"} },
		"trailing dot component":        func(f *NoiseFacts) { f.Sample = []string{"dist./a"} },
		"trailing space component":      func(f *NoiseFacts) { f.Sample = []string{"dist /a"} },
		"newline":                       func(f *NoiseFacts) { f.Sample = []string{"dist/a\nb"} },
		"carriage return":               func(f *NoiseFacts) { f.Sample = []string{"dist/a\r"} },
		"NUL":                           func(f *NoiseFacts) { f.Sample = []string{"dist/a\x00b"} },
		"C1 control":                    func(f *NoiseFacts) { f.Sample = []string{"dist/a\u0085"} },
		"invalid UTF-8":                 func(f *NoiseFacts) { f.Sample = []string{"dist/\xff"} },
		"path of 4097 bytes":            func(f *NoiseFacts) { f.Sample = []string{strings.Repeat("a", 4097)} },
		"path of 4097 bytes in unicode": func(f *NoiseFacts) { f.Sample = []string{"a" + strings.Repeat("é", 2048)} },
	} {
		t.Run(name, func(t *testing.T) {
			f := ok
			f.Sample = slices.Clone(ok.Sample)
			change(&f)
			if err := f.Validate(); err == nil {
				t.Fatalf("accepted %+v", f.Sample[:min(len(f.Sample), 2)])
			}
		})
	}
	// The exact upper bounds are accepted, and the exact bytes are what is kept.
	if err := (NoiseFacts{Repo: "rep_1", UntrackedCount: 5000, Sample: many[:5000]}).Validate(); err != nil {
		t.Errorf("5000 paths rejected: %v", err)
	}
	if err := (NoiseFacts{Repo: "rep_1", UntrackedCount: 1, Sample: []string{strings.Repeat("a", 4096)}}).Validate(); err != nil {
		t.Errorf("4096-byte path rejected: %v", err)
	}
	if err := (NoiseFacts{Repo: "rep_1", UntrackedCount: 1, Sample: []string{"a/b c/日本語/ÿ[1]#!"}}).Validate(); err != nil {
		t.Errorf("safe unicode path rejected: %v", err)
	}
}
