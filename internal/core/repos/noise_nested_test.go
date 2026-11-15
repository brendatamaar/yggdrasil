package repos

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// observe runs one status observation through the step and returns the
// candidates it suggests.
func observe(t *testing.T, f NoiseFacts) []NoiseCandidate {
	t.Helper()
	n, cs := NoiseStep(Noise{Repo: f.Repo, State: NoSuggestion{}}, NoiseObserved{Facts: f})
	if len(cs) != 0 {
		t.Fatalf("an observation produced commands: %v", cs)
	}
	if s, ok := n.State.(Suggested); ok {
		return s.Candidates
	}
	return nil
}

func candidateDirs(cs []NoiseCandidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Directory)
	}
	return out
}

func manyFiles(dir string, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%s/f%d", dir, i))
	}
	return out
}

// Only the outer directory is offered when a noise directory holds another.
func TestNoiseDropsCandidatesNestedUnderAnother(t *testing.T) {
	f := NoiseFacts{Repo: "rep_1", UntrackedCount: 200}
	f.Sample = append(manyFiles("web/node_modules", 60), manyFiles("web/node_modules/x/build", 60)...)
	if got := candidateDirs(observe(t, f)); !slices.Equal(got, []string{"web/node_modules"}) {
		t.Fatalf("candidates = %v, want only web/node_modules", got)
	}
}

// Nesting is by whole path segment: a sibling that merely shares a string
// prefix is not nested, so both stay.
func TestNoiseNestingIsBySegmentNotStringPrefix(t *testing.T) {
	f := NoiseFacts{Repo: "rep_1", UntrackedCount: 200}
	f.Sample = append(manyFiles("a/bin", 50), manyFiles("a/binX/obj", 50)...)
	if got := candidateDirs(observe(t, f)); !slices.Equal(got, []string{"a/bin", "a/binX/obj"}) {
		t.Fatalf("candidates = %v, want both a/bin and a/binX/obj", got)
	}
	if nestedUnder("a/bin", "a/bin") || nestedUnder("a/binX/obj", "a/bin") || !nestedUnder("a/bin/obj", "a/bin") || nestedUnder("a", "a/bin") {
		t.Fatal("nestedUnder is not segment based")
	}
}

// Windows separators and CRLF are rejected at the facts boundary, so they can
// never produce, or hide, a candidate.
func TestNoiseWindowsSeparatorsAndCRLFDoNotBreakNesting(t *testing.T) {
	for _, bad := range []string{`web\node_modules\x\build\f`, "web/node_modules/x/build/f\r", "web/node_modules/x/build/f\r\n"} {
		f := NoiseFacts{Repo: "rep_1", UntrackedCount: 200}
		f.Sample = append(manyFiles("web/node_modules", 60), manyFiles("web/node_modules/x/build", 59)...)
		f.Sample = append(f.Sample, bad)
		n, cs := NoiseStep(Noise{Repo: f.Repo, State: NoSuggestion{}}, NoiseObserved{Facts: f})
		if _, ok := n.State.(NoSuggestion); !ok || len(cs) != 0 {
			t.Fatalf("%q: invalid sample was analysed: %+v", bad, n)
		}
	}
}

// referenceQualifying is an independent oracle: every directory prefix whose
// last segment is allowlisted and that meets the thresholds, nested or not.
func referenceQualifying(f NoiseFacts) []string {
	counts := map[string]int{}
	for _, file := range f.Sample {
		parts := strings.Split(file, "/")
		for i := 1; i < len(parts); i++ {
			if slices.Contains(NoiseDirs(), parts[i-1]) {
				counts[strings.Join(parts[:i], "/")]++
			}
		}
	}
	var out []string
	if f.UntrackedCount < NoiseMinUntracked || len(f.Sample) < NoiseMinSample {
		return out
	}
	for d, c := range counts {
		if c >= NoiseMinSample && c >= (len(f.Sample)+1)/2 {
			out = append(out, d)
		}
	}
	return out
}

func coveredPaths(sample, directories []string) map[string]bool {
	out := map[string]bool{}
	for _, p := range sample {
		for _, d := range directories {
			if strings.HasPrefix(p, d+"/") {
				out[p] = true
			}
		}
	}
	return out
}

// Property: no returned candidate is nested under another, and the untracked
// paths the candidates cover are exactly those the unfiltered qualifying
// directories cover.
func TestPropNoiseNoNestedCandidatesAndCoverageUnchanged(t *testing.T) {
	segs := []string{"node_modules", "build", "bin", "binX", "web", "src", "dist", "x"}
	chain := []string{"web", "node_modules", "x", "build"}
	nonEmpty := 0
	for seed := int64(0); seed < 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		seen := map[string]bool{}
		f := NoiseFacts{Repo: "rep_1"}
		for i, n := 0, 50+r.Intn(150); i < n; i++ {
			var parts []string
			if r.Intn(10) < 8 { // mostly a deep chain, so that nesting really happens
				parts = slices.Clone(chain[:2+r.Intn(len(chain)-1)])
			} else {
				for j, depth := 0, 1+r.Intn(3); j < depth; j++ {
					parts = append(parts, segs[r.Intn(len(segs))])
				}
			}
			p := strings.Join(parts, "/") + fmt.Sprintf("/f%d", i)
			if !seen[p] {
				seen[p] = true
				f.Sample = append(f.Sample, p)
			}
		}
		f.UntrackedCount = len(f.Sample) + r.Intn(40)
		if err := f.Validate(); err != nil {
			t.Fatal(err)
		}
		got := candidateDirs(observe(t, f))
		if len(got) > 0 {
			nonEmpty++
		}
		for _, a := range got {
			for _, b := range got {
				if nestedUnder(a, b) {
					t.Fatalf("seed %d: %q is nested under %q in %v", seed, a, b, got)
				}
			}
		}
		want, have := coveredPaths(f.Sample, referenceQualifying(f)), coveredPaths(f.Sample, got)
		if len(want) != len(have) {
			t.Fatalf("seed %d: covered paths changed: %d before, %d after (%v)", seed, len(want), len(have), got)
		}
		for p := range want {
			if !have[p] {
				t.Fatalf("seed %d: %q no longer covered", seed, p)
			}
		}
	}
	if nonEmpty < 20 {
		t.Fatalf("generator rarely suggests anything (%d of 300): the property is vacuous", nonEmpty)
	}
}

// The sample is an alphabetical prefix whenever it is shorter than the
// untracked count or the reader truncated; every candidate says so.
func TestNoiseCandidatesCarryPartialSampleMarker(t *testing.T) {
	for _, tc := range []struct {
		count     int
		truncated bool
		partial   bool
	}{{60, false, false}, {61, false, true}, {500, false, true}, {60, true, true}, {500, true, true}} {
		f := noiseFacts(60, "dist")
		f.UntrackedCount, f.Truncated = tc.count, tc.truncated
		if f.IsPartial() != tc.partial {
			t.Fatalf("%+v: IsPartial = %v", tc, f.IsPartial())
		}
		cs := observe(t, f)
		if tc.count < NoiseMinUntracked {
			if len(cs) != 0 {
				t.Fatalf("%+v: suggested below the threshold", tc)
			}
			continue
		}
		if len(cs) != 1 || cs[0].Incomplete != tc.partial {
			t.Errorf("%+v: candidates %+v, want one with Incomplete=%v", tc, cs, tc.partial)
		}
	}
}
