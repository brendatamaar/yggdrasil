package repos

import (
	"slices"
	"testing"
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

// NoiseStep is a documented stub: it suggests nothing and creates no proposal.
func TestNoiseStepIsInert(t *testing.T) {
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
