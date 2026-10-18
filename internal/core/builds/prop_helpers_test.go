package builds

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const propRuns = 400

// forSeeds runs f with a deterministic generator per seed and names the seed
// of a failing run.
func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		ok := t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) })
		if !ok {
			return
		}
	}
}

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func timeSec(n int) time.Duration { return time.Duration(n) * time.Second }

func genSpec(r *rand.Rand) Spec {
	n := 1 + r.Intn(4)
	st := make([]StepSpec, n)
	for i := range st {
		st[i] = StepSpec{RepoID: core.RepoID(fmt.Sprintf("r%d", r.Intn(3))), Name: fmt.Sprintf("s%d", i), Argv: []string{"go", "test"}}
	}
	sp := Spec{Target: MainCheckout{}, Steps: st}
	if r.Intn(3) == 0 {
		sp.Target = InWorktree{WorktreeID: "w1"}
	}
	if r.Intn(10) == 0 {
		sp.Steps = nil // invalid: must be rejected
	}
	return sp
}

// current returns the index and attempt of the live step, if any.
func current(s State) (idx, attempt int, ok bool) {
	var run Running
	switch st := s.(type) {
	case Running:
		run = st
	case Cancelling:
		run = st.Running
	default:
		return 0, 0, false
	}
	if rs, isRun := run.Steps[run.Current].(StepRunning); isRun {
		return run.Current, rs.Attempt, true
	}
	return 0, 0, false
}

// genInput mixes legal, stale and duplicate inputs, biased by the state.
func genInput(r *rand.Rand, s State, now time.Time) Input {
	idx, att, live := current(s)
	if !live || r.Intn(3) == 0 {
		idx, att = r.Intn(5), r.Intn(3)
	}
	if live && r.Intn(5) == 0 {
		idx += r.Intn(3) - 1 // stale index
	}
	if live && r.Intn(6) == 0 {
		att++ // stale attempt
	}
	switch r.Intn(10) {
	case 0:
		return Enqueue{ID: "b1", Spec: genSpec(r), Actor: "user", Now: now}
	case 1, 2:
		return Admit{Now: now}
	case 3, 4, 5:
		code := 0
		if r.Intn(3) == 0 {
			code = 1 + r.Intn(3)
		}
		return StepExited{Index: idx, Attempt: att, ExitCode: code, LogTruncated: r.Intn(4) == 0, Now: now}
	case 6:
		reasons := []StartFailure{StartNotFound, StartBadArgs, StartDirMissing, StartOther}
		return StepStartFailed{Index: idx, Attempt: att, Reason: reasons[r.Intn(4)], Now: now}
	case 7:
		return Cancel{Actor: "user", Now: now}
	case 8:
		return ProcessLost{Now: now}
	default:
		var arts []Artifact
		if r.Intn(2) == 0 {
			arts = []Artifact{{RepoID: "r0", Path: "a.jar", Size: 1}}
		}
		return ArtifactsCollected{Artifacts: arts, Now: now}
	}
}

type checker func(prev State, in Input, next State, cmds []Command) error

// drive runs random sequences from None and calls a fresh checker per run
// after every step. Input times never go backwards.
func drive(t *testing.T, mk func() checker) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		chk := mk()
		var s State = None{}
		now := epoch
		for i := 0; i < 40; i++ {
			now = now.Add(timeSec(r.Intn(3)))
			var in Input
			if i == 0 && r.Intn(5) > 0 {
				sp := genSpec(r)
				if len(sp.Steps) == 0 {
					sp.Steps = []StepSpec{{RepoID: "r0", Name: "x", Argv: []string{"go"}}}
				}
				in = Enqueue{ID: "b1", Spec: sp, Actor: "user", Now: now}
			} else {
				in = genInput(r, s, now)
			}
			next, cmds := Step(s, in)
			if err := chk(s, in, next, cmds); err != nil {
				t.Fatalf("step %d, input %#v\nprev  %#v\nnext  %#v\ncmds  %#v\n%v", i, in, s, next, cmds, err)
			}
			s = next
		}
	})
}
