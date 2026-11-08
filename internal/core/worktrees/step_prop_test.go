package worktrees

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= 500; seed++ {
		if !t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) }) {
			return
		}
	}
}

func runOf(s State) (Run, bool) {
	switch st := s.(type) {
	case Loading:
		return st.Run, true
	case Preparing:
		return st.Run, true
	case Naming:
		return st.Run, true
	case Probing:
		return st.Run, true
	case Adding:
		return st.Run, true
	case Recording:
		return st.Run, true
	}
	return Run{}, false
}

func placedOf(s State) []core.Worktree {
	if r, ok := runOf(s); ok {
		return r.Placed
	}
	p, _, _ := Result(s)
	return p
}

// answer makes a random reply to the command, drawn from every answer the
// shell could give (including bad ones).
func answer(r *rand.Rand, c Command, n *int) Input {
	*n++
	switch v := c.(type) {
	case LoadExisting:
		return ExistingLoaded{Seq: v.Seq}
	case PrepareRepo:
		if r.Intn(8) == 0 {
			return PrepareFailed{Seq: v.Seq, Cause: []Cause{CauseGit, CauseUnusable, CauseCanceled}[r.Intn(3)]}
		}
		return Prepared{Seq: v.Seq, Base: "abc"}
	case DrawName:
		// A tiny name space makes repeats likely.
		return NameDrawn{Seq: v.Seq, Name: Name(fmt.Sprintf("calm-pine%d", r.Intn(4)))}
	case CheckName:
		switch r.Intn(10) {
		case 0:
			return ProbeFailed{Seq: v.Seq, Cause: CauseGit}
		case 1, 2, 3, 4, 5:
			return Probed{Seq: v.Seq, Taken: true}
		}
		return Probed{Seq: v.Seq}
	case AddWorktree:
		if r.Intn(8) == 0 {
			return AddFailed{Seq: v.Seq, Cause: CauseGit}
		}
		return Added{Seq: v.Seq, ID: core.WorktreeID(fmt.Sprintf("wt_%d", *n)), AuditID: core.AuditID(fmt.Sprintf("aud_%d", *n)), Path: fmt.Sprintf("/wt/%d", *n), At: epoch}
	case RecordWorktree:
		if r.Intn(8) == 0 {
			return RecordFailed{Seq: v.Seq}
		}
		return Recorded{Seq: v.Seq}
	}
	panic("unknown command")
}

func randomStale(r *rand.Rand, seq int) Input {
	bad := seq + 1 + r.Intn(3)
	if r.Intn(2) == 0 && seq > 1 {
		bad = seq - 1 - r.Intn(seq-1)
	}
	return []Input{
		ExistingLoaded{Seq: bad}, LoadFailed{Seq: bad}, Prepared{Seq: bad, Base: "x"}, PrepareFailed{Seq: bad},
		NameDrawn{Seq: bad, Name: "zzz-zzz"}, Probed{Seq: bad}, Probed{Seq: bad, Taken: true}, ProbeFailed{Seq: bad},
		Added{Seq: bad, ID: "wt_s", Path: "/s"}, AddFailed{Seq: bad}, Recorded{Seq: bad}, RecordFailed{Seq: bad},
	}[r.Intn(12)]
}

func randomRepos(r *rand.Rand) []core.RepoID {
	out := make([]core.RepoID, r.Intn(5))
	for i := range out {
		out[i] = core.RepoID(fmt.Sprintf("rep_%d", r.Intn(4))) // repeats are possible
	}
	return out
}

func randomKnown(r *rand.Rand) []Known {
	var out []Known
	for i := range r.Intn(4) {
		st := []core.WorktreeStatus{core.WorktreeActive, core.WorktreeActive, core.WorktreeMerged, core.WorktreeRemoved}[r.Intn(4)]
		out = append(out, Known{InRoot: r.Intn(4) != 0, Worktree: core.Worktree{
			ID: core.WorktreeID(fmt.Sprintf("wt_old%d", i)), RepoID: core.RepoID(fmt.Sprintf("rep_%d", r.Intn(4))),
			TaskID: "tsk_1", Name: "old-one", Path: "/old", Branch: "ygg/old-one", Status: st,
		}})
	}
	return out
}

// TestPropInvariants drives random runs and checks every invariant after
// every step.
func TestPropInvariants(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		repos := randomRepos(r)
		var s State = Idle{}
		s, cmds := Step(s, Begin{Task: "tsk_1", Repos: repos})
		counter := 0
		probes := map[core.RepoID][]Name{} // CheckName names per repo, for the current repo run
		lastSeq := 1
		lastPlaced := 0
		for step := 0; step < 200; step++ {
			if _, _, done := Result(s); done {
				break
			}
			var in Input
			stale := false
			switch {
			case r.Intn(4) == 0:
				stale = true
				run, _ := runOf(s)
				in = randomStale(r, run.Seq)
			case len(cmds) > 0:
				in = answer(r, cmds[0], &counter)
				if _, ok := cmds[0].(LoadExisting); ok {
					in = ExistingLoaded{Seq: cmds[0].(LoadExisting).Seq, Existing: randomKnown(r)}
				}
			default:
				t.Fatalf("no command outstanding in %T", s)
			}
			prev, prevCmds := s, cmds
			next, nc := Step(s, in)

			if !stale {
				cmds = nc
			} else {
				// stale input ignored: nothing changes, nothing is issued.
				if !reflect.DeepEqual(prev, next) || len(nc) != 0 {
					t.Fatalf("stale %#v changed %T or issued %v", in, prev, nc)
				}
				cmds = prevCmds
			}
			s = next
			if len(nc) > 1 {
				t.Fatalf("%d commands at once", len(nc))
			}
			if run, ok := runOf(s); ok {
				if run.Seq < lastSeq {
					t.Fatalf("Seq went back: %d -> %d", lastSeq, run.Seq)
				}
				lastSeq = run.Seq
				if len(nc) == 1 && cmdSeq(nc[0]) != run.Seq {
					t.Fatalf("command Seq %d != state Seq %d", cmdSeq(nc[0]), run.Seq)
				}
			}
			// Attempts never exceed the bound.
			switch st := s.(type) {
			case Naming:
				if st.Attempt >= MaxAttempts || len(st.Tried) > MaxAttempts {
					t.Fatalf("attempt %d, tried %v", st.Attempt, st.Tried)
				}
			case Probing:
				if st.Attempt >= MaxAttempts || len(st.Tried) > MaxAttempts {
					t.Fatalf("attempt %d, tried %v", st.Attempt, st.Tried)
				}
				// A candidate is new: not among the earlier ones.
				for _, o := range st.Tried[:len(st.Tried)-1] {
					if o == st.Candidate {
						t.Fatalf("candidate %q was tried before", o)
					}
				}
			}
			// Probes: distinct per repo run, at most MaxAttempts, and Add only after a free probe.
			for _, c := range nc {
				switch v := c.(type) {
				case CheckName:
					for _, o := range probes[v.Repo] {
						if o == v.Name {
							t.Fatalf("name %q probed twice for %s", o, v.Repo)
						}
					}
					probes[v.Repo] = append(probes[v.Repo], v.Name)
					if len(probes[v.Repo]) > MaxAttempts {
						t.Fatalf("%d probes for %s", len(probes[v.Repo]), v.Repo)
					}
					if v.Branch != v.Name.Branch() {
						t.Fatalf("branch %q for name %q", v.Branch, v.Name)
					}
				case AddWorktree:
					p, ok := prev.(Probing)
					if !ok || p.Candidate != v.Name {
						t.Fatalf("AddWorktree for %q without a free probe in %T", v.Name, prev)
					}
				}
			}
			// Placed: in request order, one worktree per repo, never shrinks.
			placed := placedOf(s)
			if len(placed) < lastPlaced || len(placed) > len(repos) {
				t.Fatalf("placed %d (was %d) of %d requested", len(placed), lastPlaced, len(repos))
			}
			lastPlaced = len(placed)
			byRepo := map[core.RepoID]core.Worktree{}
			for i, w := range placed {
				if w.RepoID != repos[i] || w.TaskID != "tsk_1" || w.Status != core.WorktreeActive {
					t.Fatalf("placed[%d] = %+v for repo %s", i, w, repos[i])
				}
				if o, ok := byRepo[w.RepoID]; ok && o != w {
					t.Fatalf("two worktrees for repo %s: %+v and %+v", w.RepoID, o, w)
				}
				byRepo[w.RepoID] = w
			}
		}
		// Finished: success places every repo; failure keeps the earlier ones.
		placed, failure, done := Result(s)
		if !done {
			return
		}
		if failure == nil && len(placed) != len(repos) {
			t.Fatalf("done with %d of %d repos", len(placed), len(repos))
		}
		if failure != nil && len(placed) >= len(repos) {
			t.Fatalf("failed %v with all %d repos placed", failure, len(repos))
		}
		if failure != nil && failure.Kind == FailNameTaken && len(probes[failure.Repo]) > MaxAttempts {
			t.Fatalf("gave up after %d probes", len(probes[failure.Repo]))
		}
	})
}

// A finished state ignores everything.
func TestPropFinishedStatesAreAbsorbing(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for _, s := range []State{Done{Placed: nil}, Failed{Failure: Failure{Kind: FailGit}}} {
			in := randomStale(r, 1+r.Intn(5))
			next, cmds := Step(s, in)
			if !reflect.DeepEqual(next, s) || len(cmds) != 0 {
				t.Fatalf("%T moved on %#v", s, in)
			}
		}
	})
}

// Exhausting names ends in FailNameTaken after exactly MaxAttempts probes,
// no matter what names arrive.
func TestPropNameBoundIsExact(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		s, cmds := Step(Idle{}, Begin{Task: "tsk_1", Repos: []core.RepoID{"rep_1"}})
		s, cmds = Step(s, ExistingLoaded{Seq: cmds[0].(LoadExisting).Seq})
		s, cmds = Step(s, Prepared{Seq: cmds[0].(PrepareRepo).Seq, Base: "abc"})
		draws, probes := 0, 0
		for len(cmds) == 1 {
			switch v := cmds[0].(type) {
			case DrawName:
				if v.Attempt != draws {
					t.Fatalf("attempt %d, want %d", v.Attempt, draws)
				}
				draws++
				// Sometimes repeat an old name: it must still count as an attempt.
				s, cmds = Step(s, NameDrawn{Seq: v.Seq, Name: Name(fmt.Sprintf("a-b%d", r.Intn(3)))})
			case CheckName:
				probes++
				s, cmds = Step(s, Probed{Seq: v.Seq, Taken: true})
			default:
				t.Fatalf("unexpected %T", v)
			}
		}
		_, f, done := Result(s)
		if !done || f == nil || f.Kind != FailNameTaken || draws != MaxAttempts || probes > MaxAttempts {
			t.Fatalf("done=%v failure=%v draws=%d probes=%d", done, f, draws, probes)
		}
	})
}

func cmdSeq(c Command) int {
	switch v := c.(type) {
	case LoadExisting:
		return v.Seq
	case PrepareRepo:
		return v.Seq
	case DrawName:
		return v.Seq
	case CheckName:
		return v.Seq
	case AddWorktree:
		return v.Seq
	case RecordWorktree:
		return v.Seq
	}
	return -1
}
