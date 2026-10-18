package repos

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Property tests of the status refresher: random input sequences, with the
// invariants R1 to R9 of docs/design/repo-status.md checked after every step.

const propRuns = 300

var propIDs = []core.RepoID{"rep_a", "rep_b", "rep_c", "rep_d", "rep_e", "rep_f"}

var propPaths = []CleanPath{"/work/one", "/work/two", "/work/three"}

var propReasons = []RefreshReason{ReasonUser, ReasonFocus, ReasonGitAction, ReasonBuild, ReasonTracking, ReasonTick}

var propEpoch = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

type attemptKey struct {
	ID      core.RepoID
	Attempt int
}

// model is the test's own history of a run. It decides which results are
// fresh from what was emitted and which repos were removed or moved since,
// never from the Refresher's InFlight or Orphans.
type model struct {
	path     map[core.RepoID]CleanPath
	emitted  map[attemptKey]bool // every ReadStatus of the whole run
	keys     []attemptKey        // emitted, in order
	latest   map[core.RepoID]int // latest ReadStatus attempt per id
	valid    map[core.RepoID]bool
	answered map[attemptKey]bool
	dups     []attemptKey // ReadStatus emitted twice in the run
}

func newModel() *model {
	return &model{
		path: map[core.RepoID]CleanPath{}, emitted: map[attemptKey]bool{}, latest: map[core.RepoID]int{},
		valid: map[core.RepoID]bool{}, answered: map[attemptKey]bool{},
	}
}

func resultKey(in RefreshInput) (attemptKey, bool) {
	switch x := in.(type) {
	case StatusRead:
		return attemptKey{x.ID, x.Attempt}, true
	case StatusFailed:
		return attemptKey{x.ID, x.Attempt}, true
	}
	return attemptKey{}, false
}

// fresh: the result answers the latest ReadStatus emitted for its id, no
// ReposChanged removed or moved the id since, and it was not answered yet.
func (m *model) fresh(k attemptKey) bool {
	return m.emitted[k] && m.latest[k.ID] == k.Attempt && m.valid[k.ID] && !m.answered[k]
}

// observe records one step: the input and the commands it produced.
func (m *model) observe(in RefreshInput, cmds []RefreshCommand) {
	switch x := in.(type) {
	case ReposChanged:
		active := map[core.RepoID]CleanPath{}
		for _, r := range x.Active {
			active[r.ID] = r.Path
		}
		for id, p := range m.path {
			if np, ok := active[id]; !ok || np != p {
				m.valid[id] = false
			}
		}
		m.path = active
	default:
		if k, ok := resultKey(in); ok && m.emitted[k] {
			m.answered[k] = true
		}
	}
	for _, c := range cmds {
		if rs, ok := c.(ReadStatus); ok {
			k := attemptKey{rs.ID, rs.Attempt}
			if m.emitted[k] {
				m.dups = append(m.dups, k)
			}
			m.emitted[k] = true
			m.keys = append(m.keys, k)
			m.latest[rs.ID] = rs.Attempt
			m.valid[rs.ID] = true
		}
	}
}

// driver feeds random inputs and hands each step to a check.
type driver struct {
	rng   *rand.Rand
	state Refresher
	now   time.Time
	m     *model
	// pre is whether the model called the input a fresh result, before the step.
	pre bool
	// readd is an id removed while its read was in flight, waiting to come back.
	readd core.RepoID
	// readdOrphan: the last input re-added an id that still had an orphan.
	readdOrphan bool
}

func newDriver(seed uint64) *driver {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	return &driver{rng: rng, state: NewRefresher(1 + rng.IntN(4)), now: propEpoch, m: newModel()}
}

// current lists the active repos now (from Entries), in ID order.
func (d *driver) current() []RepoRef {
	ids := make([]core.RepoID, 0, len(d.state.Entries))
	for id := range d.state.Entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]RepoRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, RepoRef{ID: id, Path: d.state.Entries[id].Path})
	}
	return out
}

func (d *driver) pick(ids []core.RepoID) []core.RepoID {
	var out []core.RepoID
	for _, id := range ids {
		if d.rng.IntN(2) == 0 {
			out = append(out, id)
		}
	}
	return out
}

func (d *driver) snap() Snapshot {
	s := Snapshot{
		Branch: "main", HasUpstream: d.rng.IntN(2) == 0, Upstream: "origin/main",
		Ahead: d.rng.IntN(3), Behind: d.rng.IntN(3), Staged: d.rng.IntN(2),
		Unstaged: d.rng.IntN(2), Untracked: d.rng.IntN(2), Conflicted: d.rng.IntN(2),
	}
	if d.rng.IntN(5) == 0 {
		s.Detached, s.Branch = true, ""
	}
	if d.rng.IntN(2) == 0 {
		s.LastCommit = &CommitRef{OID: fmt.Sprintf("%040d", d.rng.IntN(4)), Subject: "s", Author: "a", At: propEpoch}
	}
	return s
}

// next makes one random input. With a read in flight it often answers it; it
// sometimes sends a stale or unknown result.
func (d *driver) next() RefreshInput {
	d.now = d.now.Add(time.Duration(d.rng.IntN(90)) * time.Second)
	flight := make([]core.RepoID, 0, len(d.state.InFlight))
	for id := range d.state.InFlight {
		flight = append(flight, id)
	}
	sort.Slice(flight, func(i, j int) bool { return flight[i] < flight[j] })
	d.readdOrphan = false
	switch k := d.rng.IntN(12); {
	case k < 2:
		var active []RepoRef
		for _, id := range d.pick(propIDs) {
			active = append(active, RepoRef{ID: id, Path: propPaths[d.rng.IntN(len(propPaths))]})
		}
		return ReposChanged{Active: active}
	case k < 4:
		ids := d.pick(propIDs)
		if d.rng.IntN(4) == 0 {
			ids = append(ids, "rep_unknown")
		}
		return RefreshRequested{IDs: ids, Reason: propReasons[d.rng.IntN(len(propReasons))], Now: d.now}
	case k < 5:
		return Tick{Now: d.now}
	case k < 8 && len(flight) > 0:
		id := flight[d.rng.IntN(len(flight))]
		return d.result(id, d.state.InFlight[id])
	case k == 8 && len(flight) > 0:
		// Remove a repo while its read is in flight; it comes back later.
		id := flight[d.rng.IntN(len(flight))]
		d.readd = id
		var active []RepoRef
		for _, r := range d.current() {
			if r.ID != id {
				active = append(active, r)
			}
		}
		return ReposChanged{Active: active}
	case k == 9 && d.readd != "":
		id := d.readd
		d.readd = ""
		_, orphan := d.state.Orphans[id]
		d.readdOrphan = orphan
		active := append(d.current(), RepoRef{ID: id, Path: propPaths[d.rng.IntN(len(propPaths))]})
		return ReposChanged{Active: active}
	case k == 10 && len(d.m.keys) > 0:
		// A late result of any read ever emitted.
		key := d.m.keys[d.rng.IntN(len(d.m.keys))]
		return d.result(key.ID, key.Attempt)
	default:
		id := propIDs[d.rng.IntN(len(propIDs)+1)%len(propIDs)]
		if d.rng.IntN(5) == 0 {
			id = "rep_unknown"
		}
		return d.result(id, d.rng.IntN(d.state.Seq+2)-1)
	}
}

func (d *driver) result(id core.RepoID, attempt int) RefreshInput {
	if d.rng.IntN(3) == 0 {
		return StatusFailed{ID: id, Attempt: attempt, Kind: ProblemGitFailed, Now: d.now}
	}
	return StatusRead{ID: id, Attempt: attempt, Snap: d.snap(), Now: d.now}
}

// run drives n steps and calls check(before, input, after, cmds) after each.
func run(t *testing.T, n int, check func(t *testing.T, d *driver, before Refresher, in RefreshInput, cmds []RefreshCommand)) {
	t.Helper()
	for seed := uint64(1); seed <= propRuns; seed++ {
		d := newDriver(seed)
		for i := 0; i < n; i++ {
			in := d.next()
			before := d.state
			var cmds []RefreshCommand
			d.state, cmds = Refresh(before, in)
			if t.Failed() {
				return
			}
			check(t, d, before, in, cmds)
			if t.Failed() {
				t.Logf("seed %d step %d input %#v", seed, i, in)
				return
			}
		}
	}
}

func staleResult(r Refresher, in RefreshInput) bool {
	var id core.RepoID
	var attempt int
	switch x := in.(type) {
	case StatusRead:
		id, attempt = x.ID, x.Attempt
	case StatusFailed:
		id, attempt = x.ID, x.Attempt
	default:
		return false
	}
	a, busy := r.InFlight[id]
	_, known := r.Entries[id]
	return !known || !busy || a != attempt
}

// R1: the reads in flight never exceed Max.
func TestPropInFlightBounded(t *testing.T) {
	run(t, 80, func(t *testing.T, d *driver, _ Refresher, _ RefreshInput, _ []RefreshCommand) {
		if len(d.state.InFlight) > d.state.Max {
			t.Fatalf("%d reads in flight, Max %d", len(d.state.InFlight), d.state.Max)
		}
	})
}

// R2: Queue is set-like; Queue and InFlight only name repos that have an entry.
func TestPropQueueSetLike(t *testing.T) {
	run(t, 80, func(t *testing.T, d *driver, _ Refresher, _ RefreshInput, _ []RefreshCommand) {
		seen := map[core.RepoID]bool{}
		for _, id := range d.state.Queue {
			if seen[id] {
				t.Fatalf("%s is in the queue twice: %v", id, d.state.Queue)
			}
			seen[id] = true
			if _, ok := d.state.Entries[id]; !ok {
				t.Fatalf("queued %s has no entry", id)
			}
		}
		for id := range d.state.InFlight {
			if _, ok := d.state.Entries[id]; !ok {
				t.Fatalf("in-flight %s has no entry", id)
			}
		}
	})
}

// R3: work-conserving. A free slot with a repo waiting is only allowed when
// that repo is itself being read (it is read again once that read ends).
func TestPropNoIdleWhileQueued(t *testing.T) {
	run(t, 80, func(t *testing.T, d *driver, _ Refresher, _ RefreshInput, _ []RefreshCommand) {
		if len(d.state.InFlight) >= d.state.Max {
			return
		}
		for _, id := range d.state.Queue {
			if _, busy := d.state.InFlight[id]; !busy {
				t.Fatalf("slot free (%d of %d) while %s waits", len(d.state.InFlight), d.state.Max, id)
			}
		}
	})
}

// R4: a stale or unknown result changes nothing and emits nothing.
func TestPropStaleResultIgnored(t *testing.T) {
	stale := 0
	run(t, 80, func(t *testing.T, d *driver, before Refresher, in RefreshInput, cmds []RefreshCommand) {
		if !staleResult(before, in) {
			return
		}
		stale++
		if len(cmds) != 0 {
			t.Fatalf("stale result emitted %v", cmds)
		}
		if !reflect.DeepEqual(before, d.state) {
			t.Fatalf("stale result changed the state:\n before %#v\n after  %#v", before, d.state)
		}
	})
	if stale == 0 {
		t.Fatal("the generator never produced a stale result")
	}
}

// R5: a ReadStatus for (id, a) is emitted at most once per life of the entry,
// and a grows by one per start of that ID.
func TestPropAttemptsMonotonic(t *testing.T) {
	starts := 0
	run(t, 80, func(t *testing.T, d *driver, before Refresher, _ RefreshInput, cmds []RefreshCommand) {
		for _, c := range cmds {
			rs, ok := c.(ReadStatus)
			if !ok {
				continue
			}
			starts++
			prev := before.Entries[rs.ID].Attempt
			if rs.Attempt != prev+1 {
				t.Fatalf("ReadStatus %s attempt %d, want %d", rs.ID, rs.Attempt, prev+1)
			}
			if d.state.InFlight[rs.ID] != rs.Attempt {
				t.Fatalf("%s started attempt %d but InFlight holds %d", rs.ID, rs.Attempt, d.state.InFlight[rs.ID])
			}
		}
		seen := map[[2]any]bool{}
		for _, c := range cmds {
			if rs, ok := c.(ReadStatus); ok {
				k := [2]any{rs.ID, rs.Attempt}
				if seen[k] {
					t.Fatalf("ReadStatus %v emitted twice in one step", k)
				}
				seen[k] = true
			}
		}
	})
	if starts == 0 {
		t.Fatal("no read was ever started")
	}
}

// R6: Problem.Last is the most recent Fresh snapshot of that repo, or nil.
func TestPropProblemKeepsLast(t *testing.T) {
	model := map[*driver]map[core.RepoID]*Snapshot{}
	problems := 0
	run(t, 100, func(t *testing.T, d *driver, before Refresher, in RefreshInput, _ []RefreshCommand) {
		if model[d] == nil {
			model[d] = map[core.RepoID]*Snapshot{}
		}
		m := model[d]
		for id, e := range before.Entries {
			ne, ok := d.state.Entries[id]
			if !ok || ne.Path != e.Path {
				delete(m, id)
			}
		}
		if x, ok := in.(StatusRead); ok && !staleResult(before, in) {
			s := x.Snap
			m[x.ID] = &s
		}
		for id, e := range d.state.Entries {
			p, ok := e.View.(Problem)
			if !ok {
				continue
			}
			problems++
			want := m[id]
			if (p.Last == nil) != (want == nil) || (want != nil && !snapEqual(*p.Last, *want)) {
				t.Fatalf("%s: Problem.Last = %+v, want %+v", id, p.Last, want)
			}
		}
	})
	if problems == 0 {
		t.Fatal("the generator never produced a Problem view")
	}
}

// R7: Counts equal a brute-force recount from Flags and Snapshot.
func TestPropCountsMatchFlags(t *testing.T) {
	for seed := uint64(1); seed <= propRuns; seed++ {
		d := newDriver(seed)
		entries := map[core.RepoID]Entry{}
		failed := map[core.RepoID]struct{}{}
		var want Counts
		for _, id := range propIDs {
			var v View
			switch d.rng.IntN(3) {
			case 0:
				v = Unknown{}
			case 1:
				v = Fresh{Snap: d.snap(), At: propEpoch}
			default:
				p := Problem{Kind: []ProblemKind{ProblemMissing, ProblemNotARepo, ProblemTimedOut, ProblemGitFailed}[d.rng.IntN(4)], At: propEpoch}
				if d.rng.IntN(2) == 0 {
					s := d.snap()
					p.Last = &s
				}
				v = p
			}
			if d.rng.IntN(2) == 0 {
				continue
			}
			entries[id] = Entry{Path: "/work/x", View: v}
			if d.rng.IntN(3) == 0 {
				failed[id] = struct{}{}
				want.FailedPipeline++
			}
			want.All++
			var s *Snapshot
			switch x := v.(type) {
			case Fresh:
				s = &x.Snap
			case Problem:
				s = x.Last
			}
			if s != nil {
				if s.Staged > 0 || s.Unstaged > 0 || s.Untracked > 0 || s.Conflicted > 0 {
					want.Uncommitted++
				}
				if s.Behind > 0 {
					want.Behind++
				}
				if s.Ahead > 0 {
					want.Ahead++
				}
			}
			bad := false
			switch x := v.(type) {
			case Problem:
				bad = true
			case Fresh:
				bad = x.Snap.Detached || !x.Snap.HasUpstream || x.Snap.Conflicted > 0 || (x.Snap.Ahead > 0 && x.Snap.Behind > 0)
			}
			if bad {
				want.Problems++
			}
			if bad != (len(Flags(v)) > 0) {
				t.Fatalf("seed %d: Flags(%+v) = %v, bad = %v", seed, v, Flags(v), bad)
			}
		}
		if got := CountEntries(entries, failed); got != want {
			t.Fatalf("seed %d: CountEntries = %+v, want %+v", seed, got, want)
		}
		for _, f := range []Filter{FilterAll, FilterUncommitted, FilterBehind, FilterAhead, FilterFailedPipeline, FilterProblems} {
			n := 0
			for id, e := range entries {
				_, fp := failed[id]
				if f.Matches(e.View, fp) {
					n++
				}
			}
			var w int
			switch f {
			case FilterAll:
				w = want.All
			case FilterUncommitted:
				w = want.Uncommitted
			case FilterBehind:
				w = want.Behind
			case FilterAhead:
				w = want.Ahead
			case FilterFailedPipeline:
				w = want.FailedPipeline
			case FilterProblems:
				w = want.Problems
			}
			if n != w {
				t.Fatalf("seed %d: filter %s matches %d, count says %d", seed, f, n, w)
			}
		}
	}
}

// R8: liveness. Answering every ReadStatus empties Queue and InFlight, and
// every user-requested repo that stays active ends with a view read at or
// after the request.
func TestPropEveryRequestServed(t *testing.T) {
	for seed := uint64(1); seed <= propRuns; seed++ {
		d := newDriver(seed)
		requested := map[core.RepoID]time.Time{}
		var pending []ReadStatus
		for i := 0; i < 60; i++ {
			in := d.next()
			switch x := in.(type) {
			case StatusRead, StatusFailed:
				continue // results come only from the drain below
			case RefreshRequested:
				x.Reason = ReasonUser
				in = x
				for _, id := range x.IDs {
					if _, ok := d.state.Entries[id]; ok {
						requested[id] = x.Now
					}
				}
				if len(x.IDs) == 0 {
					for id := range d.state.Entries {
						requested[id] = x.Now
					}
				}
			case ReposChanged:
				for id := range requested {
					found := false
					for _, r := range x.Active {
						if r.ID == id {
							found = true
						}
					}
					if !found {
						delete(requested, id)
					}
				}
			}
			var cmds []RefreshCommand
			d.state, cmds = Refresh(d.state, in)
			for _, c := range cmds {
				if rs, ok := c.(ReadStatus); ok {
					pending = append(pending, rs)
				}
			}
		}
		for steps := 0; len(pending) > 0 || len(d.state.Queue) > 0 || len(d.state.InFlight) > 0; steps++ {
			if steps > 1000 || len(pending) == 0 {
				t.Fatalf("seed %d: no progress: queue %v in flight %v pending %v", seed, d.state.Queue, d.state.InFlight, pending)
			}
			rs := pending[0]
			pending = pending[1:]
			d.now = d.now.Add(time.Second)
			var cmds []RefreshCommand
			var in RefreshInput = StatusRead{ID: rs.ID, Attempt: rs.Attempt, Snap: d.snap(), Now: d.now}
			if rs.Attempt%3 == 0 {
				in = StatusFailed{ID: rs.ID, Attempt: rs.Attempt, Kind: ProblemTimedOut, Now: d.now}
			}
			d.state, cmds = Refresh(d.state, in)
			for _, c := range cmds {
				if n, ok := c.(ReadStatus); ok {
					pending = append(pending, n)
				}
			}
		}
		for id, at := range requested {
			e, ok := d.state.Entries[id]
			if !ok {
				continue
			}
			vat, has := ViewAt(e.View)
			if !has || vat.Before(at) {
				t.Fatalf("seed %d: %s requested at %v has view %#v", seed, id, at, e.View)
			}
		}
	}
}

// R9: PublishCounts is emitted exactly when the counts changed.
func TestPropCountsPublishedOnChange(t *testing.T) {
	changes := 0
	run(t, 100, func(t *testing.T, d *driver, before Refresher, _ RefreshInput, cmds []RefreshCommand) {
		published := false
		for _, c := range cmds {
			if _, ok := c.(PublishCounts); ok {
				published = true
			}
		}
		changed := CountEntries(before.Entries, nil) != CountEntries(d.state.Entries, nil)
		if changed {
			changes++
		}
		if published != changed {
			t.Fatalf("PublishCounts emitted = %v, counts changed = %v", published, changed)
		}
	})
	if changes == 0 {
		t.Fatal("the counts never changed")
	}
}

// Refresh never changes the state it was given.
func TestPropRefreshIsPure(t *testing.T) {
	run(t, 60, func(t *testing.T, d *driver, before Refresher, in RefreshInput, _ []RefreshCommand) {
		snapshot := fmt.Sprintf("%+v", before)
		_, _ = Refresh(before, in)
		if snapshot != fmt.Sprintf("%+v", before) {
			t.Fatal("Refresh changed its input state")
		}
	})
}
