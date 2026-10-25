package search

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const propRuns = 300

func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		if !t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) }) {
			return
		}
	}
}

func genRepos(r *rand.Rand) []RepoRef {
	n := r.Intn(9) // 0 repos is a case
	names := []string{"shop-api", "shop-web", "shop-admin"}
	out := make([]RepoRef, n)
	for i := range out {
		// Names collide on purpose: the order must still be total.
		out[i] = RepoRef{ID: core.RepoID(fmt.Sprintf("r%d", i)), Name: names[r.Intn(len(names))], Path: "/x"}
	}
	return out
}

func genMatches(r *rand.Rand, repo core.RepoID) []Match {
	var n int
	switch r.Intn(6) {
	case 0:
		n = 0
	case 1, 2, 3:
		n = 230 + r.Intn(100) // around the per-repo cap
	default:
		n = r.Intn(30)
	}
	ms := make([]Match, n)
	for i := range ms {
		ms[i] = Match{
			Repo: core.RepoID(fmt.Sprintf("r%d", r.Intn(6))), // may be wrong; Step stamps it
			Path: fmt.Sprintf("src/f%d.go", r.Intn(40)),
			Line: 1 + r.Intn(400),
			Col:  1 + r.Intn(3),
			Text: fmt.Sprintf("t%d", r.Intn(3)),
		}
	}
	return ms
}

func genInputs(r *rand.Rand, repos []RepoRef) []Input {
	var ins []Input
	answer := func(id core.RepoID) Input {
		if r.Intn(8) == 0 {
			return RepoFailed{Repo: id, Kind: []ProblemKind{ProblemGitFailed, ProblemMissing, ProblemTimedOut}[r.Intn(3)]}
		}
		return RepoResult{Repo: id, Matches: genMatches(r, id), Truncated: r.Intn(8) == 0}
	}
	for _, rr := range repos {
		ins = append(ins, answer(rr.ID))
	}
	// Noise: repeated answers and answers from repos that were never asked.
	for range r.Intn(4) {
		ins = append(ins, answer(core.RepoID(fmt.Sprintf("r%d", r.Intn(12)))))
	}
	r.Shuffle(len(ins), func(i, j int) { ins[i], ins[j] = ins[j], ins[i] })
	if r.Intn(6) == 0 {
		ins = slices.Insert(ins, r.Intn(len(ins)+1), Input(Deadline{}))
	}
	return ins
}

// stepAll runs the inputs and calls check after every step.
func stepAll(t *testing.T, repos []RepoRef, ins []Input, check func(Search)) Search {
	t.Helper()
	s, _ := Step(Search{}, Begin{Query: Query{Pattern: "x", Mode: ModeLiteral}, Repos: repos})
	check(s)
	for _, in := range ins {
		s, _ = Step(s, in)
		check(s)
	}
	return s
}

// C1: caps hold after every step.
func TestPropCapsHold(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		repos := genRepos(r)
		stepAll(t, repos, genInputs(r, repos), func(s Search) {
			if len(s.Results) > MaxTotal {
				t.Fatalf("%d results, cap %d", len(s.Results), MaxTotal)
			}
			per := map[core.RepoID]int{}
			for _, m := range s.Results {
				per[m.Repo]++
			}
			for id, n := range per {
				if n > MaxPerRepo || s.PerRepo[id] != n {
					t.Fatalf("repo %s: %d results, PerRepo %d, cap %d", id, n, s.PerRepo[id], MaxPerRepo)
				}
			}
		})
	})
}

// C2: results are sorted strictly by (repo name, repo id, path, line, col). The
// oracle is a field-by-field comparator that does not use the production one.
func TestPropResultsOrdered(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		repos := genRepos(r)
		names := map[core.RepoID]string{}
		for _, rr := range repos {
			names[rr.ID] = rr.Name
		}
		// less is strict "a sorts before b".
		less := func(a, b Match) bool {
			if na, nb := names[a.Repo], names[b.Repo]; na != nb {
				return na < nb
			}
			if a.Repo != b.Repo {
				return a.Repo < b.Repo
			}
			if a.Path != b.Path {
				return a.Path < b.Path
			}
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			return a.Col < b.Col
		}
		stepAll(t, repos, genInputs(r, repos), func(s Search) {
			for i := 1; i < len(s.Results); i++ {
				a, b := s.Results[i-1], s.Results[i]
				if !less(a, b) {
					t.Fatalf("results %d and %d out of order or duplicate: %+v then %+v", i-1, i, a, b)
				}
			}
			for id, n := range s.PerRepo {
				got := 0
				for _, m := range s.Results {
					if m.Repo == id {
						got++
					}
				}
				if got != n || n == 0 {
					t.Fatalf("stale PerRepo[%s] = %d, results have %d", id, n, got)
				}
			}
		})
	})
}

type key struct {
	path      string
	line, col int
}

// C3: Truncated is true exactly when a cap cut something or a worker said so.
// The oracle recomputes it from the inputs that a pending repo accepted.
func TestPropTruncatedHonest(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		repos := genRepos(r)
		ins := genInputs(r, repos)
		pending := map[core.RepoID]bool{}
		for _, rr := range repos {
			pending[rr.ID] = true
		}
		want := false
		kept := map[core.RepoID][]key{}
		for _, in := range ins {
			switch x := in.(type) {
			case RepoResult:
				if !pending[x.Repo] || len(pending) == 0 {
					continue
				}
				delete(pending, x.Repo)
				want = want || x.Truncated
				set := map[key]bool{}
				for _, m := range x.Matches {
					set[key{m.Path, m.Line, m.Col}] = true
				}
				ks := make([]key, 0, len(set))
				for k := range set {
					ks = append(ks, k)
				}
				if len(ks) > MaxPerRepo {
					want = true
					slices.SortFunc(ks, func(a, b key) int {
						if a.path != b.path {
							return compare(Match{Path: a.path}, Match{Path: b.path}, nil)
						}
						return compare(Match{Line: a.line, Col: a.col}, Match{Line: b.line, Col: b.col}, nil)
					})
					ks = ks[:MaxPerRepo]
				}
				kept[x.Repo] = ks
				total := 0
				for _, k := range kept {
					total += len(k)
				}
				if total > MaxTotal {
					want = true
					// Later inputs cannot undo a cut: the union only grows.
				}
			case RepoFailed:
				delete(pending, x.Repo)
			case Deadline:
				clear(pending)
			}
		}
		got := stepAll(t, repos, ins, func(Search) {})
		if got.Truncated != want {
			t.Fatalf("Truncated = %v, want %v", got.Truncated, want)
		}
	})
}

// C4: the order in which repos answer does not change the final result.
func TestPropMergeOrderIndependent(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		repos := genRepos(r)
		var ins []Input
		for _, rr := range repos {
			if r.Intn(6) == 0 {
				ins = append(ins, RepoFailed{Repo: rr.ID, Kind: ProblemMissing})
				continue
			}
			ins = append(ins, RepoResult{Repo: rr.ID, Matches: genMatches(r, rr.ID), Truncated: r.Intn(8) == 0})
		}
		a := stepAll(t, repos, ins, func(Search) {})
		shuffled := slices.Clone(ins)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		b := stepAll(t, repos, shuffled, func(Search) {})
		if !slices.Equal(a.Results, b.Results) || a.Truncated != b.Truncated || !equalMaps(a.Problems, b.Problems) || !equalMaps(a.PerRepo, b.PerRepo) {
			t.Fatalf("arrival order changed the outcome:\n%+v\n%+v", a, b)
		}
	})
}

func equalMaps[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// C5: once done, every repo of Begin is in exactly one bucket. The oracle
// records the first input each repo had accepted and predicts the bucket.
func TestPropEveryRepoAccounted(t *testing.T) {
	for _, finalDeadline := range []bool{true, false} {
		t.Run(fmt.Sprintf("finalDeadline=%v", finalDeadline), func(t *testing.T) {
			forSeeds(t, func(t *testing.T, r *rand.Rand) {
				repos := genRepos(r)
				ins := genInputs(r, repos)
				if finalDeadline {
					ins = append(ins, Deadline{})
				}
				asked := map[core.RepoID]bool{}
				for _, rr := range repos {
					asked[rr.ID] = true
				}
				type first struct {
					failed bool
					kind   ProblemKind
				}
				firsts := map[core.RepoID]first{}
				timedOut := map[core.RepoID]bool{}
				deadline := false
				for _, in := range ins {
					if deadline {
						break // everything after the deadline is ignored
					}
					switch x := in.(type) {
					case RepoResult:
						if _, ok := firsts[x.Repo]; asked[x.Repo] && !ok {
							firsts[x.Repo] = first{}
						}
					case RepoFailed:
						if _, ok := firsts[x.Repo]; asked[x.Repo] && !ok {
							firsts[x.Repo] = first{failed: true, kind: x.Kind}
						}
					case Deadline:
						// A deadline only acts while somebody is pending.
						if len(firsts) < len(asked) {
							deadline = true
							for id := range asked {
								if _, ok := firsts[id]; !ok {
									timedOut[id] = true
								}
							}
						}
					}
				}
				s := stepAll(t, repos, ins, func(Search) {})
				wantDone := len(firsts) == len(asked) || deadline
				if s.Done() != wantDone {
					t.Fatalf("Done = %v, want %v", s.Done(), wantDone)
				}
				has := map[core.RepoID]bool{}
				for _, m := range s.Results {
					has[m.Repo] = true
				}
				for id := range asked {
					f, answered := firsts[id]
					kind, problem := s.Problems[id]
					switch {
					case answered && !f.failed:
						if problem || s.Pending[id] {
							t.Fatalf("repo %s answered with a result but problem=%v pending=%v", id, problem, s.Pending[id])
						}
					case answered && f.failed:
						if kind != f.kind || has[id] || s.Pending[id] {
							t.Fatalf("repo %s failed with %s, got problem %q matches=%v pending=%v", id, f.kind, kind, has[id], s.Pending[id])
						}
					case timedOut[id]:
						if kind != ProblemTimedOut || has[id] || s.Pending[id] {
							t.Fatalf("repo %s never answered: want timed_out, got %q matches=%v pending=%v", id, kind, has[id], s.Pending[id])
						}
					default: // still waiting, no deadline
						if !s.Pending[id] || problem || has[id] {
							t.Fatalf("repo %s should be pending only", id)
						}
					}
				}
				for id := range s.Problems {
					if !asked[id] {
						t.Fatalf("problem for unknown repo %s", id)
					}
				}
			})
		})
	}
}

// Commands are data: Begin gives one Grep per distinct repo, a Deadline gives
// CancelAll once, and a stray input changes nothing.
func TestStepTransitions(t *testing.T) {
	q := Query{Pattern: "x", Mode: ModeLiteral}
	repos := []RepoRef{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}, {ID: "a", Name: "a"}}
	s, cmds := Step(Search{}, Begin{Query: q, Repos: repos})
	if len(cmds) != 2 || !s.Searching() {
		t.Fatalf("begin: %d commands, searching=%v", len(cmds), s.Searching())
	}
	if s2, c := Step(s, RepoResult{Repo: "zzz"}); c != nil || len(s2.Pending) != 2 {
		t.Fatal("unknown repo changed the state")
	}
	if _, c := Step(s, Begin{Query: q, Repos: repos}); c != nil {
		t.Fatal("second Begin did something")
	}
	s, c := Step(s, Deadline{})
	if len(c) != 1 || !s.Done() || s.Problems["a"] != ProblemTimedOut || s.Problems["b"] != ProblemTimedOut {
		t.Fatalf("deadline: %+v %v", s, c)
	}
	if _, c := Step(s, Deadline{}); c != nil {
		t.Fatal("deadline after done did something")
	}
	e, c := Step(Search{}, Begin{Query: q})
	if len(c) != 0 || !e.Done() {
		t.Fatal("no repos should be done at once")
	}
	if _, c := Step(Search{}, RepoResult{Repo: "a"}); c != nil {
		t.Fatal("input before Begin did something")
	}
}

func TestTextTrimmedTo300Runes(t *testing.T) {
	long := ""
	for range 400 {
		long += "é"
	}
	s, _ := Step(Search{}, Begin{Repos: []RepoRef{{ID: "a", Name: "a"}}})
	s, _ = Step(s, RepoResult{Repo: "a", Matches: []Match{{Path: "p", Line: 1, Col: 1, Text: long}}})
	if got := []rune(s.Results[0].Text); len(got) != MaxTextRunes {
		t.Fatalf("%d runes", len(got))
	}
}
