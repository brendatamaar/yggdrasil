package standup

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
)

var t0 = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

func minute(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }

// Invariants of the pure selection, checked over random inputs: the window is
// half-open, at most max come back, the order is newest first with ties by ID,
// and truncated is true exactly when more than max matched.
func TestNewestBuildsProperty(t *testing.T) {
	for seed := uint64(0); seed < 400; seed++ {
		r := rand.New(rand.NewPCG(seed, 11))
		var in []corestandup.Build
		for i := 0; i < r.IntN(40); i++ {
			in = append(in, corestandup.Build{Repo: "rep_a", ID: core.BuildID(fmt.Sprintf("bld_%02d", r.IntN(60))), At: minute(r.IntN(6))})
		}
		max := 1 + r.IntN(12)
		got, cut := newestBuilds(in, max)
		if len(got) > max || (len(in) > max) != cut || (!cut && len(got) != len(in)) {
			t.Fatalf("seed %d: %d in, max %d, %d out, cut %v", seed, len(in), max, len(got), cut)
		}
		if !slices.IsSortedFunc(got, func(a, b corestandup.Build) int {
			switch {
			case a.At.After(b.At):
				return -1
			case a.At.Before(b.At):
				return 1
			}
			return compareStrings(string(a.ID), string(b.ID))
		}) {
			t.Fatalf("seed %d: not newest first with ties by ID: %v", seed, got)
		}
		// Whatever was cut is not newer than what was kept.
		if cut && len(got) > 0 {
			last := got[len(got)-1]
			for _, b := range in {
				if b.At.After(last.At) && !slices.ContainsFunc(got, func(g corestandup.Build) bool { return g == b }) {
					t.Fatalf("seed %d: a newer build %v was cut while %v was kept", seed, b, last)
				}
			}
		}
	}
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestNewestMRsProperty(t *testing.T) {
	for seed := uint64(0); seed < 400; seed++ {
		r := rand.New(rand.NewPCG(seed, 13))
		var in []corestandup.MR
		for i := 0; i < r.IntN(40); i++ {
			in = append(in, corestandup.MR{Repo: "rep_a", Number: 1 + r.IntN(30), Title: "t", State: "open", At: minute(r.IntN(6))})
		}
		max := 1 + r.IntN(12)
		got, cut := newestMRs(in, max)
		distinct := map[int]bool{}
		for _, m := range in {
			distinct[m.Number] = true
		}
		if len(got) > max || (len(distinct) > max) != cut || (!cut && len(got) != len(distinct)) {
			t.Fatalf("seed %d: %d distinct, max %d, %d out, cut %v", seed, len(distinct), max, len(got), cut)
		}
		seen := map[int]bool{}
		for i, m := range got {
			if seen[m.Number] {
				t.Fatalf("seed %d: number %d twice", seed, m.Number)
			}
			seen[m.Number] = true
			if i > 0 && (m.At.After(got[i-1].At) || (m.At.Equal(got[i-1].At) && m.Number < got[i-1].Number)) {
				t.Fatalf("seed %d: order broken at %d: %v", seed, i, got)
			}
		}
	}
}

// buildOf over every instant around the window edges: a build is in exactly
// when its start or its finish is in [from, to).
func TestBuildOfWindowEdges(t *testing.T) {
	from, to := minute(10), minute(20)
	sp := corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: []corebuilds.StepSpec{{RepoID: "rep_a", Name: "v", Argv: []string{"x"}}}}
	for s := -1; s <= 21; s++ {
		for f := s; f <= 22; f++ {
			start := minute(s + 10)
			end := minute(f + 10)
			st := corebuilds.Finished{ID: "bld_1", Spec: sp, QueuedAt: minute(0), Actor: "user", StartedAt: &start, FinishedAt: end,
				Steps:  []corebuilds.StepState{corebuilds.StepSkipped{}},
				Result: corebuilds.ResultSucceeded{}}
			in := func(x time.Time) bool { return !x.Before(from) && x.Before(to) }
			want := in(start) || in(end)
			b, ok, err := buildOf(st, "rep_a", from, to)
			if err != nil || ok != want {
				t.Fatalf("start %v end %v: ok %v, want %v, err %v", start, end, ok, want, err)
			}
			if ok && !in(b.At) {
				t.Fatalf("start %v end %v: At %v is outside the window", start, end, b.At)
			}
		}
	}
}

func TestBuildOfRejectsInvalidStates(t *testing.T) {
	sp := corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: []corebuilds.StepSpec{{RepoID: "rep_a", Name: "v", Argv: []string{"x"}}}}
	mk := func(id core.BuildID, spec corebuilds.Spec) corebuilds.State {
		return corebuilds.Running{ID: id, Spec: spec, QueuedAt: minute(0), StartedAt: minute(12), Actor: "user"}
	}
	for name, st := range map[string]corebuilds.State{
		"no id":         mk("", sp),
		"other repo":    mk("bld_1", corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: []corebuilds.StepSpec{{RepoID: "rep_b", Name: "v", Argv: []string{"x"}}}}),
		"no steps":      mk("bld_1", corebuilds.Spec{Target: corebuilds.MainCheckout{}}),
		"none state":    corebuilds.None{},
		"finished none": corebuilds.Finished{ID: "bld_1", Spec: sp, FinishedAt: minute(12)},
	} {
		if _, _, err := buildOf(st, "rep_a", minute(10), minute(20)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
