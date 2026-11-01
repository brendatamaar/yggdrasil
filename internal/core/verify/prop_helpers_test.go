package verify

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const (
	propRuns  = 300
	propSteps = 60
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

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

func newAggregate() Verification {
	return Verification{ID: "v1", Task: "t1", Repo: "r1", Worktree: "w1", State: None{}}
}

// gen produces inputs and remembers the build IDs it issued, so it can send
// stale and duplicate deliveries.
type gen struct {
	r      *rand.Rand
	nextID int
	issued []core.BuildID
	last   Input
	now    time.Time
	turn   int
}

func (g *gen) tick() time.Time {
	g.now = g.now.Add(time.Second)
	return g.now
}

func (g *gen) fresh() core.BuildID {
	g.nextID++
	id := core.BuildID(fmt.Sprintf("b%d", g.nextID))
	g.issued = append(g.issued, id)
	return id
}

func (g *gen) steps(prefix string, n int) []StepSpec {
	out := make([]StepSpec, n)
	for i := range out {
		out[i] = StepSpec{Name: fmt.Sprintf("%s%d", prefix, i), Argv: []string{"go", "test"}}
	}
	return out
}

func (g *gen) plan() Plan {
	p := Plan{
		Bootstrap: g.steps("boot", g.r.Intn(3)),
		Verify:    g.steps("check", 1+g.r.Intn(3)),
		DepsHash:  []string{"h1", "h2", "h3"}[g.r.Intn(3)],
	}
	if g.r.Intn(10) == 0 {
		p.Verify = nil
	}
	if g.r.Intn(25) == 0 {
		p.Verify = []StepSpec{{Name: "", Argv: nil}} // invalid: must be ignored
	}
	return p
}

func (g *gen) facts() Facts {
	ok := func() bool { return g.r.Intn(8) != 0 }
	return Facts{Enabled: ok(), Trusted: ok(), WorktreeActive: ok(), HasChanges: ok()}
}

// turnLabel is an opaque random label: it carries no order.
func turnLabel(r *rand.Rand) TurnID { return TurnID(fmt.Sprintf("%x", r.Int63())) }

func (g *gen) result(chainLen int) BuildResult {
	step := g.r.Intn(chainLen+2) - 1 // -1 and chainLen are out of range
	switch g.r.Intn(8) {
	case 0, 1, 2:
		return BuildPassed{}
	case 3, 4:
		return BuildFailedAt{Step: step, ExitCode: g.r.Intn(3)} // exit code 0 is invalid
	case 5:
		return BuildStartFailedAt{Step: step, Reason: StartFailure([]string{"npm was not found", ""}[g.r.Intn(2)])}
	case 6:
		return BuildCancelled{}
	}
	return BuildInterrupted{}
}

// next mixes legal, stale and duplicate inputs, biased by the state.
func (g *gen) next(v Verification) Input {
	if g.last != nil && g.r.Intn(8) == 0 {
		return g.last // duplicate delivery
	}
	live, isLive := liveBuild(v.State)
	var in Input
	switch k := g.r.Intn(12); {
	case k <= 2:
		d := g.r.Intn(4) - 1 // -1 stale, 0 same, 1..2 newer
		g.turn += max(d, 0)
		n := g.turn
		if d < 0 {
			n--
		}
		in = TurnEnded{Turn: turnLabel(g.r), Seq: n, Facts: g.facts(), Plan: g.plan(), BootBuild: g.fresh(), VerifyBuild: g.fresh(), Now: g.tick()}
	case k == 3:
		in = Retry{Facts: g.facts(), Plan: g.plan(), BootBuild: g.fresh(), VerifyBuild: g.fresh(), Now: g.tick()}
	case k == 4:
		in = Resume{Facts: g.facts(), Plan: g.plan(), BootBuild: g.fresh(), VerifyBuild: g.fresh(), Now: g.tick()}
	case k == 5:
		in = Cancel{Now: g.tick()}
	case k == 6:
		b := core.BuildID("nope")
		if isLive && g.r.Intn(2) == 0 {
			b = live
		}
		in = EnqueueFailed{Build: b, Reason: "rejected", Now: g.tick()}
	default:
		b := core.BuildID("nope")
		switch {
		case isLive && g.r.Intn(10) < 7:
			b = live
		case len(g.issued) > 0:
			b = g.issued[g.r.Intn(len(g.issued))] // stale or duplicate
		}
		chain := 2
		switch s := v.State.(type) {
		case Bootstrapping:
			chain = len(s.Plan.Bootstrap)
		case Verifying:
			chain = len(s.Plan.Verify)
		}
		in = BuildFinished{Build: b, Result: g.result(chain), Now: g.tick()}
	}
	g.last = in
	return in
}

// run drives the aggregate through random inputs and calls check after every
// step.
func run(t *testing.T, r *rand.Rand, check func(t *testing.T, before Verification, in Input, after Verification, cmds []Command)) {
	t.Helper()
	g := &gen{r: r, now: epoch, turn: 1}
	v := newAggregate()
	for i := 0; i < propSteps; i++ {
		in := g.next(v)
		after, cmds := Step(v, in)
		check(t, v, in, after, cmds)
		if t.Failed() {
			t.Fatalf("step %d: %T failed the invariant (state %T -> %T)", i, in, v.State, after.State)
		}
		v = after
	}
}

func changed(a, b Verification) bool { return !reflect.DeepEqual(a, b) }

func count[T Command](cmds []Command) (n int) {
	for _, c := range cmds {
		if _, ok := c.(T); ok {
			n++
		}
	}
	return n
}

func isTerminal(s State) bool {
	switch s.(type) {
	case Passed, Failed, Skipped, Unverifiable, Interrupted, Cancelled:
		return true
	}
	return false
}

func isLiveState(s State) bool { _, ok := liveBuild(s); return ok }
