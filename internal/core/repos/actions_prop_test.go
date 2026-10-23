package repos

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Property tests of the git actions: random input sequences with the
// invariants A1 to A8 of docs/design/git-actions.md checked after every step.

const actRuns = 300

var actRepos = []core.RepoID{"rep_a", "rep_b", "rep_c", "rep_d", "rep_e", "rep_f"}

var actEpoch = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

type itemKey struct {
	Batch BatchID
	Repo  core.RepoID
}

type attKey struct {
	Batch   BatchID
	Repo    core.RepoID
	Attempt int
}

// actModel is the test's own history. It never reads the state to decide what
// an invariant should be.
type actModel struct {
	kind      map[BatchID]ActionKind
	started   map[itemKey]bool // CheckStatus or RunGit emitted
	marked    map[itemKey]bool // busy before the item started
	okCheck   map[attKey]bool  // a clean StatusChecked with the branch was fed
	auditDone map[itemKey]int
	auditBeg  map[BatchID]int
	open      []ActCommand // emitted, not answered
	violation string
	only      string // when set, only violations tagged with it count ("A1" ...)
}

func newActModel() *actModel {
	return &actModel{
		kind: map[BatchID]ActionKind{}, started: map[itemKey]bool{}, marked: map[itemKey]bool{},
		okCheck: map[attKey]bool{}, auditDone: map[itemKey]int{}, auditBeg: map[BatchID]int{},
	}
}

func (m *actModel) fail(f string, a ...any) {
	if m.only != "" && !strings.HasPrefix(f, m.only+":") {
		return
	}
	if m.violation == "" {
		m.violation = fmt.Sprintf(f, a...)
	}
}

// before records what the input says, ahead of the step.
func (m *actModel) before(st Actions, in ActInput) {
	switch x := in.(type) {
	case StartBatch:
		if x.Validate() == nil {
			if _, dup := st.Batches[x.ID]; !dup {
				m.kind[x.ID] = x.Kind
				for _, r := range x.Repos {
					if _, busy := x.Busy[r]; busy {
						m.marked[itemKey{x.ID, r}] = true
					}
				}
			}
		}
	case BuildStarted:
		for id, b := range st.Batches {
			for _, it := range b.Items {
				k := itemKey{id, it.Repo}
				for _, r := range x.Repos {
					if r == it.Repo && !m.started[k] {
						m.marked[k] = true
					}
				}
			}
		}
	case StatusChecked:
		if !x.Dirty && x.HasBranch {
			m.okCheck[attKey{x.Batch, x.Repo, x.Attempt}] = true
		}
	}
}

// after checks the commands of one step.
func (m *actModel) after(st Actions, cmds []ActCommand) {
	for _, c := range cmds {
		switch x := c.(type) {
		case CheckStatus:
			m.started[itemKey{x.Batch, x.Repo}] = true
			m.open = append(m.open, x)
		case RunGit:
			k := itemKey{x.Batch, x.Repo}
			m.started[k] = true
			m.open = append(m.open, x)
			kind := m.kind[x.Batch]
			if x.Op != OpFetch && x.Op != OpPull && x.Op != OpCheckout { // A8
				m.fail("A8: RunGit with op %q", x.Op)
			}
			if want := map[ActionKind]GitOp{KindFetch: "fetch", KindPull: "pull", KindCheckout: "checkout", KindSwitch: "checkout"}[kind]; want != x.Op {
				m.fail("A8: batch kind %s ran op %s", kind, x.Op)
			}
			if kind.ChangesCheckout() && m.marked[k] { // A3
				m.fail("A3: %s ran for %v although a build was running before it started", x.Op, k)
			}
			if kind == KindSwitch && !m.okCheck[attKey{x.Batch, x.Repo, x.Attempt}] { // A4
				m.fail("A4: switch checkout for %v attempt %d without a clean status check", k, x.Attempt)
			}
		case AuditBatchStart:
			m.auditBeg[x.Batch]++
		case AuditRepoDone:
			m.auditDone[itemKey{x.Batch, x.Repo}]++
		case RefreshRepoStatus, PublishBatch:
		default: // A8
			m.fail("A8: unexpected command %T", c)
		}
	}
	m.state(st)
}

func (m *actModel) state(st Actions) {
	active := map[core.RepoID]BatchID{}
	n := 0
	for id, b := range st.Batches {
		if m.auditBeg[id] != 1 { // A6
			m.fail("A6: batch %s has %d start audit entries", id, m.auditBeg[id])
		}
		for _, it := range b.Items {
			k := itemKey{id, it.Repo}
			switch it.State.(type) {
			case Checking, Running:
				n++
				if other, dup := active[it.Repo]; dup { // A1
					m.fail("A1: repo %s active in %s and %s", it.Repo, other, id)
				}
				active[it.Repo] = id
				if st.Running[it.Repo] != id {
					m.fail("A1: Running[%s] = %q, item is in %s", it.Repo, st.Running[it.Repo], id)
				}
			case Done:
				if m.auditDone[k] != 1 { // A6
					m.fail("A6: done item %v has %d audit entries", k, m.auditDone[k])
				}
			default:
				if m.auditDone[k] != 0 {
					m.fail("A6: item %v has an audit entry but is not done", k)
				}
			}
		}
	}
	if len(st.Running) != n { // A1
		m.fail("A1: Running has %d entries for %d active items", len(st.Running), n)
	}
	if n > st.Max { // A2
		m.fail("A2: %d items active, Max is %d", n, st.Max)
	}
	if n < st.Max { // A7
		for id, b := range st.Batches {
			for _, it := range b.Items {
				if _, p := it.State.(Pending); !p {
					continue
				}
				if _, locked := st.Running[it.Repo]; !locked {
					m.fail("A7: item %s/%s is Pending with a free slot and a free repo", id, it.Repo)
				}
			}
		}
	}
}

type actDriver struct {
	r     *rand.Rand
	st    Actions
	m     *actModel
	now   time.Time
	seq   int
	ids   []BatchID
	trace []string
}

func newActDriver(seed uint64, max int) *actDriver {
	return &actDriver{r: rand.New(rand.NewPCG(seed, 7)), st: NewActions(max), m: newActModel(), now: actEpoch}
}

func (d *actDriver) feed(in ActInput) {
	d.trace = append(d.trace, fmt.Sprintf("%T%+v", in, in))
	d.m.before(d.st, in)
	next, cmds := Act(d.st, in)
	d.st = next
	d.m.after(d.st, cmds)
}

func (d *actDriver) tick() time.Time {
	d.now = d.now.Add(time.Duration(1+d.r.IntN(30)) * time.Second)
	return d.now
}

func (d *actDriver) pickRepos(n int) []core.RepoID {
	p := d.r.Perm(len(actRepos))
	out := make([]core.RepoID, 0, n)
	for _, i := range p[:n] {
		out = append(out, actRepos[i])
	}
	return out
}

func (d *actDriver) startBatch() {
	d.seq++
	id := BatchID(fmt.Sprintf("gab_%d", d.seq))
	kinds := []ActionKind{KindFetch, KindPull, KindCheckout, KindSwitch}
	kind := kinds[d.r.IntN(len(kinds))]
	n := 1 + d.r.IntN(len(actRepos))
	branch := ""
	if kind == KindCheckout {
		n = 1
	}
	if kind.NeedsBranch() {
		branch = "feature"
	}
	busy := map[core.RepoID]string{}
	for _, r := range actRepos {
		if d.r.IntN(5) == 0 {
			busy[r] = "a build is running in this repo"
		}
	}
	d.ids = append(d.ids, id)
	d.feed(StartBatch{ID: id, Kind: kind, Branch: branch, Repos: d.pickRepos(n), Busy: busy, Actor: "user", Now: d.tick()})
}

// answer feeds the result of one open command, or a stale one.
func (d *actDriver) answer() bool {
	if len(d.m.open) == 0 {
		return false
	}
	i := d.r.IntN(len(d.m.open))
	c := d.m.open[i]
	d.m.open = append(d.m.open[:i], d.m.open[i+1:]...)
	switch x := c.(type) {
	case CheckStatus:
		switch d.r.IntN(6) {
		case 0:
			d.feed(RepoGone{Batch: x.Batch, Repo: x.Repo, Attempt: x.Attempt, Now: d.tick()})
		case 1:
			d.feed(ActionDone{Batch: x.Batch, Repo: x.Repo, Attempt: x.Attempt, Outcome: Failed{Kind: FailGit}, Now: d.tick()})
		default:
			d.feed(StatusChecked{Batch: x.Batch, Repo: x.Repo, Attempt: x.Attempt,
				Dirty: d.r.IntN(3) == 0, HasBranch: d.r.IntN(4) != 0, Now: d.tick()})
		}
	case RunGit:
		outs := []ActOutcome{Fetched{}, Pulled{From: "a", To: "b"}, UpToDate{}, PullRefused{Reason: PullDiverged},
			CheckedOut{Branch: "feature"}, CheckoutRefused{Reason: CheckoutDirtyConflict}, Failed{Kind: FailNetwork}}
		if d.r.IntN(10) == 0 {
			d.feed(RepoGone{Batch: x.Batch, Repo: x.Repo, Attempt: x.Attempt, Now: d.tick()})
		} else {
			d.feed(ActionDone{Batch: x.Batch, Repo: x.Repo, Attempt: x.Attempt, Outcome: outs[d.r.IntN(len(outs))], Now: d.tick()})
		}
	}
	return true
}

func (d *actDriver) step() {
	switch k := d.r.IntN(20); {
	case k < 4:
		d.startBatch()
	case k < 5 && len(d.ids) > 0:
		d.feed(CancelBatch{ID: d.ids[d.r.IntN(len(d.ids))], Actor: "user", Now: d.tick()})
	case k < 6:
		d.feed(BuildStarted{Repos: d.pickRepos(1 + d.r.IntN(3)), Now: d.tick()})
	case k < 7 && len(d.ids) > 0: // stale or unknown answers
		d.feed(StatusChecked{Batch: d.ids[d.r.IntN(len(d.ids))], Repo: actRepos[d.r.IntN(len(actRepos))],
			Attempt: d.r.IntN(4), Dirty: d.r.IntN(2) == 0, HasBranch: d.r.IntN(2) == 0, Now: d.tick()})
	case k < 8 && len(d.ids) > 0:
		d.feed(ActionDone{Batch: d.ids[d.r.IntN(len(d.ids))], Repo: actRepos[d.r.IntN(len(actRepos))],
			Attempt: d.r.IntN(4), Outcome: Fetched{}, Now: d.tick()})
	case k < 9:
		d.now = d.now.Add(time.Duration(d.r.IntN(15)) * time.Minute)
		d.feed(ActSweep{Now: d.now})
	default:
		d.answer()
	}
}

func (d *actDriver) check(t *testing.T, seed uint64) {
	t.Helper()
	if d.m.violation != "" {
		n := len(d.trace)
		from := max(0, n-6)
		t.Fatalf("seed %d: %s\nlast inputs:\n%v", seed, d.m.violation, d.trace[from:n])
	}
}

func runAct(t *testing.T, steps int, each func(d *actDriver)) {
	t.Helper()
	runActOnly(t, "", steps, each)
}

// runActOnly runs the same random sequences but counts only the invariant
// named by tag ("A1" ...), so each test fails for its own invariant alone.
func runActOnly(t *testing.T, tag string, steps int, each func(d *actDriver)) {
	t.Helper()
	for seed := uint64(1); seed <= actRuns; seed++ {
		d := newActDriver(seed, 1+int(seed%5))
		d.m.only = tag
		for i := 0; i < steps; i++ {
			d.step()
			d.check(t, seed)
		}
		if each != nil {
			each(d)
			d.check(t, seed)
		}
	}
}

func TestPropOneActionPerRepo(t *testing.T) { runActOnly(t, "A1", 80, nil) } // A1

func TestPropActionsBounded(t *testing.T) { runActOnly(t, "A2", 80, nil) } // A2

func TestPropNoGitWhileBuildRuns(t *testing.T) { runActOnly(t, "A3", 80, nil) } // A3

func TestPropSwitchOnlyClean(t *testing.T) { runActOnly(t, "A4", 80, nil) } // A4

func TestPropAuditPerAction(t *testing.T) { runActOnly(t, "A6", 80, nil) } // A6

func TestPropNoIdleWhilePending(t *testing.T) { runActOnly(t, "A7", 80, nil) } // A7

func TestPropOnlySafeGit(t *testing.T) { runActOnly(t, "A8", 80, nil) } // A8

// A5: once every command is answered, and the user cancels what is left,
// every item is Done or Skipped. Cancel never leaves an item Pending.
func TestPropBatchesFinish(t *testing.T) {
	runAct(t, 60, func(d *actDriver) {
		for _, id := range d.ids {
			d.feed(CancelBatch{ID: id, Actor: "user", Now: d.tick()})
			if b, ok := d.st.Batches[id]; ok {
				for _, it := range b.Items {
					if _, p := it.State.(Pending); p {
						d.m.fail("A5: item %s/%s still Pending after CancelBatch", id, it.Repo)
					}
				}
			}
		}
		for guard := 0; d.answer(); guard++ {
			if guard > 10000 {
				d.m.fail("A5: answers never end")
				return
			}
		}
		// Items still Pending were started by a free slot after cancel only if
		// they were not cancelled: none may remain.
		for id, b := range d.st.Batches {
			if !b.Finished() {
				d.m.fail("A5: batch %s is not finished after every command was answered: %+v", id, b.Items)
			}
		}
		if len(d.st.Running) != 0 {
			d.m.fail("A5: %d repo locks left", len(d.st.Running))
		}
	})
}

// Without cancel, answering everything also finishes every batch (liveness
// of the queue itself).
func TestPropQueueDrains(t *testing.T) {
	runAct(t, 60, func(d *actDriver) {
		for guard := 0; d.answer(); guard++ {
			if guard > 10000 {
				d.m.fail("A5: answers never end")
				return
			}
		}
		for id, b := range d.st.Batches {
			if !b.Finished() {
				d.m.fail("A5: batch %s is not finished: %+v", id, b.Items)
			}
		}
	})
}

// Act does not change its input and gives the same result twice.
func TestPropActIsPure(t *testing.T) {
	for seed := uint64(1); seed <= 100; seed++ {
		d := newActDriver(seed, 3)
		for i := 0; i < 60; i++ {
			d.step()
			before := d.st
			snapshot := before.clone()
			in := ActSweep{Now: d.now.Add(time.Hour)}
			a1, c1 := Act(before, in)
			a2, c2 := Act(before, in)
			if !reflect.DeepEqual(before, snapshot) {
				t.Fatalf("seed %d: Act changed its input", seed)
			}
			if !reflect.DeepEqual(a1, a2) || !reflect.DeepEqual(c1, c2) {
				t.Fatalf("seed %d: Act is not deterministic", seed)
			}
		}
	}
}

// Targeted cases for the transition table.
func TestActTable(t *testing.T) {
	now := actEpoch
	st := NewActions(2)
	st, cmds := Act(st, StartBatch{ID: "b1", Kind: KindSwitch, Branch: "dev", Repos: []core.RepoID{"r1", "r2", "r3"},
		Busy: map[core.RepoID]string{"r2": "a build is running in this repo"}, Actor: "user", Now: now})
	var kinds []string
	for _, c := range cmds {
		kinds = append(kinds, fmt.Sprintf("%T", c))
	}
	sort.Strings(kinds)
	want := []string{"repos.AuditBatchStart", "repos.CheckStatus", "repos.CheckStatus", "repos.PublishBatch"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("commands = %v, want %v", kinds, want)
	}
	if s := st.Batches["b1"].Items[1].State; s != (Skipped{Reason: SkipBuildRunning, At: now}) {
		t.Fatalf("busy item = %#v", s)
	}
	// dirty -> skipped, lock released
	st, _ = Act(st, StatusChecked{Batch: "b1", Repo: "r1", Attempt: 1, Dirty: true, HasBranch: true, Now: now})
	if s, ok := st.Batches["b1"].Items[0].State.(Skipped); !ok || s.Reason != SkipDirty {
		t.Fatalf("dirty item = %#v", st.Batches["b1"].Items[0].State)
	}
	if _, locked := st.Running["r1"]; locked {
		t.Fatal("lock of a skipped item kept")
	}
	// stale attempt changes nothing
	same, cmds := Act(st, StatusChecked{Batch: "b1", Repo: "r3", Attempt: 9, HasBranch: true, Now: now})
	if len(cmds) != 0 || !reflect.DeepEqual(same, st) {
		t.Fatal("stale StatusChecked changed the state")
	}
	// clean with branch -> RunGit checkout
	_, cmds = Act(st, StatusChecked{Batch: "b1", Repo: "r3", Attempt: 1, HasBranch: true, Now: now})
	var ran bool
	for _, c := range cmds {
		if g, ok := c.(RunGit); ok && g.Op == OpCheckout && g.Branch == "dev" && g.Repo == "r3" {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("no checkout after a clean check: %v", cmds)
	}
}

func TestStartBatchValidate(t *testing.T) {
	ok := StartBatch{ID: "b", Kind: KindFetch, Repos: []core.RepoID{"r"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []StartBatch{
		{ID: "b", Kind: "force", Repos: []core.RepoID{"r"}},
		{ID: "b", Kind: KindFetch},
		{ID: "b", Kind: KindFetch, Branch: "x", Repos: []core.RepoID{"r"}},
		{ID: "b", Kind: KindPull, Branch: "x", Repos: []core.RepoID{"r"}},
		{ID: "b", Kind: KindCheckout, Repos: []core.RepoID{"r"}},
		{ID: "b", Kind: KindSwitch, Repos: []core.RepoID{"r"}},
		{ID: "b", Kind: KindCheckout, Branch: "x", Repos: []core.RepoID{"r", "s"}},
		{ID: "b", Kind: KindFetch, Repos: []core.RepoID{"r", "r"}},
		{Kind: KindFetch, Repos: []core.RepoID{"r"}},
	}
	for i, b := range bad {
		if b.Validate() == nil {
			t.Errorf("case %d accepted: %+v", i, b)
		}
	}
}
