package changesets

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const (
	propRuns  = 300
	propSteps = 80
)

var (
	epoch    = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setRepos = []core.RepoID{"a", "b", "c"}
	allKinds = []core.ProposalKind{
		core.ProposalCommit, core.ProposalPush, core.ProposalMergeRequest, core.ProposalInstall, core.ProposalPublish,
		core.ProposalAgentFile, core.ProposalDeleteFiles, core.ProposalSubtasks, core.ProposalMergeLocal, core.ProposalRemoveWorktree,
	}
)

func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		if !t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) }) {
			return
		}
	}
}

func hx(i int) string { return strings.Repeat(fmt.Sprintf("%x", i%16), 64) }

func newSet(order ...core.RepoID) Open {
	if len(order) == 0 {
		order = slices.Clone(setRepos)
	}
	return Open{
		Set:     Header{ID: "cs1", Task: "t1", Repos: slices.Clone(setRepos), Order: order},
		LastRun: RunIdle{},
	}
}

// cgen models the proposals world around one change set and produces inputs
// mixing legal, stale, duplicate and invalid ones.
type cgen struct {
	r      *rand.Rand
	now    time.Time
	world  []Member
	nextID int
	hashes int
	last   Input
}

func (g *cgen) tick() time.Time {
	g.now = g.now.Add(time.Second)
	return g.now
}

func (g *cgen) newMember() Member {
	g.nextID++
	g.hashes++
	m := Member{
		ID:     core.ProposalID(fmt.Sprintf("p%d", g.nextID)),
		Repo:   setRepos[g.r.Intn(len(setRepos))],
		Kind:   allKinds[g.r.Intn(len(allKinds))],
		Status: core.ProposalPending,
		Hash:   hx(g.hashes),
	}
	if g.r.Intn(4) == 0 {
		m.Kind = []core.ProposalKind{core.ProposalCommit, core.ProposalPush}[g.r.Intn(2)]
	}
	return m
}

func advance(r *rand.Rand, m *Member) {
	switch m.Status {
	case core.ProposalPending:
		m.Status = []core.ProposalStatus{core.ProposalConfirmed, core.ProposalConfirmed, core.ProposalRejected}[r.Intn(3)]
	case core.ProposalConfirmed:
		m.Status = []core.ProposalStatus{core.ProposalExecuted, core.ProposalExecuted, core.ProposalExecuted, core.ProposalFailed, core.ProposalRejected}[r.Intn(5)]
	case core.ProposalFailed:
		if r.Intn(3) == 0 {
			m.Status = core.ProposalPending // retried through the proposals service
		}
	}
}

var linkSamples = []string{
	"https://git.example.test/acme/shop-api/-/merge_requests/1",
	"https://other.example.test/x/-/merge_requests/2",
	"javascript:alert(1)",
	"https://user:tok@git.example.test/x",
	"http://git.example.test/a b",
	"https://git.example.test/a\nb",
}

func (g *cgen) links() map[core.RepoID]string {
	out := map[core.RepoID]string{}
	for _, repo := range append(slices.Clone(setRepos), "zz") {
		if g.r.Intn(2) == 0 {
			out[repo] = linkSamples[g.r.Intn(len(linkSamples))]
		}
	}
	return out
}

func (g *cgen) membersChanged(s State) Input {
	ms := slices.Clone(g.world)
	for i := g.r.Intn(8) / 6; i > 0; i-- {
		ms = append(ms, g.newMember())
	}
	if c, ok := s.(Confirming); ok && g.r.Intn(10) < 7 {
		for i := range ms {
			if ms[i].ID == c.Current {
				advance(g.r, &ms[i])
				if ms[i].Status == core.ProposalConfirmed {
					advance(g.r, &ms[i])
				}
			}
		}
	}
	for i := g.r.Intn(5); i > 0 && len(ms) > 0; i-- {
		j := g.r.Intn(len(ms))
		if ms[j].Status == core.ProposalPending && g.r.Intn(4) == 0 {
			g.hashes++
			ms[j].Hash = hx(g.hashes) // the user edited the proposal
			continue
		}
		advance(g.r, &ms[j])
	}
	valid := true
	switch g.r.Intn(20) {
	case 0:
		if len(ms) > 0 {
			ms = append(ms, ms[0]) // duplicate ID
			valid = false
		}
	case 1:
		ms = append(ms, Member{ID: "alien", Repo: "zz", Kind: core.ProposalCommit, Status: core.ProposalPending, Hash: hx(1)})
		valid = false
	case 2:
		if len(ms) > 0 {
			ms[0].Hash = "short"
			valid = false
		}
	}
	if valid {
		g.world = slices.Clone(ms)
	}
	return MembersChanged{Members: ms, Links: g.links(), Now: g.tick()}
}

func (g *cgen) scope() Scope {
	switch g.r.Intn(6) {
	case 0:
		return nil
	case 1:
		return ScopeRepo{Repo: "zz"}
	case 2, 3:
		return ScopeRepo{Repo: setRepos[g.r.Intn(len(setRepos))]}
	}
	return ScopeAll{}
}

func (g *cgen) deps() []Edge {
	var out []Edge
	for i := g.r.Intn(4); i > 0; i-- {
		out = append(out, Edge{Dependent: setRepos[g.r.Intn(3)], Dependency: setRepos[g.r.Intn(3)]})
	}
	return out
}

func (g *cgen) confirmRun(s State) Input {
	sc := g.scope()
	seen := map[core.ProposalID]string{}
	if sc != nil {
		for _, p := range Plan(s, sc) {
			seen[p.ID] = p.Hash
		}
	}
	switch g.r.Intn(8) {
	case 0:
		for k := range seen {
			delete(seen, k)
			break
		}
	case 1:
		for k := range seen {
			seen[k] = hx(99)
			break
		}
	case 2:
		seen["ghost"] = hx(5)
	}
	var ack []WarningCode
	switch g.r.Intn(4) {
	case 0, 1:
		ack = []WarningCode{WarningDependentBeforeDependency}
	case 2:
		ack = []WarningCode{"bogus"}
	}
	return ConfirmRun{Scope: sc, Seen: seen, Ack: ack, Deps: g.deps(), Now: g.tick()}
}

func (g *cgen) order() []core.RepoID {
	o := slices.Clone(setRepos)
	g.r.Shuffle(len(o), func(i, j int) { o[i], o[j] = o[j], o[i] })
	switch g.r.Intn(6) {
	case 0:
		return o[:2]
	case 1:
		return append(o, "a")
	case 2:
		o[0] = "zz"
	}
	return o
}

func (g *cgen) next(s State) Input {
	if g.last != nil && g.r.Intn(8) == 0 {
		return g.last
	}
	var in Input
	switch k := g.r.Intn(20); {
	case k <= 6:
		in = g.membersChanged(s)
	case k <= 10:
		in = g.confirmRun(s)
	case k <= 12:
		in = SetOrder{Order: g.order(), Deps: g.deps(), Now: g.tick()}
	case k == 13:
		in = StopRun{Now: g.tick()}
	case k <= 15:
		id := core.ProposalID("p1")
		if c, ok := s.(Confirming); ok && g.r.Intn(3) > 0 {
			id = c.Current
		}
		in = ConfirmRefused{ID: id, Reason: "stale\nhash", Now: g.tick()}
	default:
		in = Close{Now: g.tick()}
	}
	g.last = in
	return in
}

type checkFn = func(t *testing.T, before State, in Input, after State, cmds []Command)

// run drives a fresh set through random inputs and calls check after every
// step.
func run(t *testing.T, r *rand.Rand, check checkFn) {
	t.Helper()
	g := &cgen{r: r, now: epoch}
	order := slices.Clone(setRepos)
	r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	var s State = newSet(order...)
	for i := 0; i < propSteps; i++ {
		in := g.next(s)
		after, cmds := StepSet(s, in)
		if err := after.Validate(); err != nil {
			t.Fatalf("step %d: %T produced an invalid state: %v", i, in, err)
		}
		check(t, s, in, after, cmds)
		if t.Failed() {
			t.Fatalf("step %d: %T failed the invariant (state %T -> %T)", i, in, s, after)
		}
		s = after
	}
}

func prop(t *testing.T, check checkFn) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) { run(t, r, check) })
}

func changedState(a, b State) bool { return !reflect.DeepEqual(a, b) }

func count[T Command](cmds []Command) (n int) {
	for _, c := range cmds {
		if _, ok := c.(T); ok {
			n++
		}
	}
	return n
}

func isLanding(k core.ProposalKind) bool {
	return k == core.ProposalMergeLocal || k == core.ProposalRemoveWorktree
}
