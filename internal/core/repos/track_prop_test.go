package repos

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Property tests T1-T9 of docs/design/repo-tracking.md. A seeded generator
// drives random input sequences through Decide; the invariants are checked
// after every step. The shell's effects are simulated between steps: builds
// start and finish (Busy) and the user trusts repos (Trusted).

type rng struct{ s uint64 }

func (r *rng) n(k int) int {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return int((r.s >> 33) % uint64(k))
}

type sim struct {
	r   *rng
	ws  Workspace
	seq int
	now time.Time
}

var (
	simPaths = []CleanPath{`/w`, `/w/a`, `/w/a/b`, `/w/c`, `/x`, `C:\Work\a`, `c:/work/A`, `C:\Work\a\b`, `C:\Work\c`}
	simNames = []string{"p1", "p2", "p3", "p4"}
	simRels  = []RelPath{".", "api", "web", "libs/x", "libs/y"}
	simRepos = []string{"api", "web", "x", "libs/x"}
)

func newSim(seed int) *sim {
	return &sim{r: &rng{s: uint64(seed)*2654435761 + 1}, ws: Workspace{Projects: map[core.ProjectID]Project{}},
		now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (s *sim) fresh(prefix string) string { s.seq++; return fmt.Sprintf("%s_%d", prefix, s.seq) }

func (s *sim) picks() []Pick {
	n := s.r.n(4)
	var out []Pick
	for i := 0; i < n; i++ {
		out = append(out, Pick{
			RelPath:   simRels[s.r.n(len(simRels))],
			Name:      simRepos[s.r.n(len(simRepos))],
			NewRepoID: core.RepoID(s.fresh("rep")),
		})
	}
	return out
}

func (s *sim) pickProject() core.ProjectID {
	ids := projectIDs(s.ws)
	if len(ids) == 0 || s.r.n(10) == 0 {
		return core.ProjectID("prj_unknown")
	}
	return ids[s.r.n(len(ids))]
}

func (s *sim) pickRepo() core.RepoID {
	var all []core.RepoID
	for _, pid := range projectIDs(s.ws) {
		all = append(all, repoIDs(s.ws.Projects[pid])...)
	}
	if len(all) == 0 || s.r.n(10) == 0 {
		return core.RepoID("rep_unknown")
	}
	return all[s.r.n(len(all))]
}

func (s *sim) trackProject() TrackProject {
	return TrackProject{
		NewProjectID: core.ProjectID(s.fresh("prj")), Name: simNames[s.r.n(len(simNames))],
		Path: simPaths[s.r.n(len(simPaths))], Repos: s.picks(), Actor: "user", Now: s.now,
	}
}

func (s *sim) input() Input {
	switch k := s.r.n(100); {
	case k < 40:
		return s.trackProject()
	case k < 60:
		return TrackRepos{ProjectID: s.pickProject(), Repos: s.picks(), Actor: "user", Now: s.now}
	case k < 75:
		return UntrackRepo{RepoID: s.pickRepo(), Actor: "user", Now: s.now}
	case k < 85:
		return UntrackProject{ProjectID: s.pickProject(), Actor: "user", Now: s.now}
	default:
		return RenameProject{ProjectID: s.pickProject(), Name: simNames[s.r.n(len(simNames))], Actor: "user", Now: s.now}
	}
}

// shell simulates the effects between two decisions: builds start and end,
// the user trusts a repo.
func (s *sim) shell() {
	busy := map[core.RepoID]BusyReason{}
	for _, pid := range projectIDs(s.ws) {
		p := s.ws.Projects[pid]
		p2 := copyRepos(p)
		for _, id := range repoIDs(p) {
			r := p.Repos[id]
			if isActive(r.Life) && s.r.n(5) == 0 {
				busy[id] = BusyBuildRunning
			}
			if isActive(r.Life) && s.r.n(3) == 0 {
				r.Trusted = true
				p2.Repos[id] = r
			}
		}
		s.ws = withProject(s.ws, p2)
	}
	s.ws.Busy = busy
}

func (s *sim) step() (before, after Workspace, in Input, cmds []Command, out Outcome) {
	s.now = s.now.Add(time.Minute)
	before, in = s.ws, s.input()
	snapshot := cloneWS(before)
	after, cmds, out = Decide(before, in)
	if !reflect.DeepEqual(before, snapshot) {
		panic("Decide changed its argument")
	}
	s.ws = after
	return
}

func cloneWS(ws Workspace) Workspace {
	c := Workspace{}
	if ws.Projects != nil {
		c.Projects = map[core.ProjectID]Project{}
	}
	if ws.Busy != nil {
		c.Busy = map[core.RepoID]BusyReason{}
	}
	for k, p := range ws.Projects {
		if p.Repos != nil {
			p = copyRepos(p)
		}
		c.Projects[k] = p
	}
	for k, v := range ws.Busy {
		c.Busy[k] = v
	}
	return c
}

const (
	seeds = 300
	steps = 60
)

// stats guards against a generator that never reaches the interesting cases.
type stats map[string]int

func (st stats) note(out Outcome, in Input) {
	switch o := out.(type) {
	case Tracked:
		st["tracked"]++
		if len(o.Restored) > 0 {
			st["restored"]++
		}
	case AlreadyTracked:
		st["already"]++
	case NameTaken:
		st["nametaken"]++
	case Overlaps:
		st["overlaps"]++
	case RepoBusy:
		st["busy"]++
	case UntrackDone:
		if len(o.Repos) > 0 {
			st["untracked"]++
		}
	case InvalidInput:
		st["invalid"]++
	}
}

func (st stats) need(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if st[k] < 10 {
			t.Errorf("the generator reached %q only %d times; the property test is too weak", k, st[k])
		}
	}
}

// forEachStep runs the generator and calls check after every step.
func forEachStep(t *testing.T, check func(t *testing.T, before, after Workspace, in Input, cmds []Command, out Outcome)) stats {
	t.Helper()
	st := stats{}
	for seed := 1; seed <= seeds; seed++ {
		s := newSim(seed)
		for i := 0; i < steps; i++ {
			before, after, in, cmds, out := s.step()
			st.note(out, in)
			check(t, before, after, in, cmds, out)
			if t.Failed() {
				t.Fatalf("seed %d, step %d, input %#v", seed, i, in)
			}
			s.shell()
		}
	}
	return st
}

func key(p CleanPath) string { return strings.ToLower(strings.ReplaceAll(string(p), `\`, "/")) }

func nested(a, b CleanPath) bool {
	x, y := key(a), key(b)
	return x == y || strings.HasPrefix(x, y+"/") || strings.HasPrefix(y, x+"/")
}

func TestPropActiveProjectsDisjoint(t *testing.T) { // T1
	st := forEachStep(t, func(t *testing.T, _, ws Workspace, _ Input, _ []Command, _ Outcome) {
		for a, pa := range ws.Projects {
			for b, pb := range ws.Projects {
				if a >= b || !isActive(pa.Life) || !isActive(pb.Life) {
					continue
				}
				if pa.Name == pb.Name {
					t.Errorf("active projects %s and %s share the name %q", a, b, pa.Name)
				}
				if nested(pa.Path, pb.Path) {
					t.Errorf("active projects %s (%s) and %s (%s) overlap", a, pa.Path, b, pb.Path)
				}
			}
		}
	})
	st.need(t, "tracked", "nametaken", "overlaps")
}

func TestPropRepoKeysUnique(t *testing.T) { // T2
	st := forEachStep(t, func(t *testing.T, _, ws Workspace, _ Input, _ []Command, _ Outcome) {
		for pid, p := range ws.Projects {
			names, rels := map[string]core.RepoID{}, map[RelPath]core.RepoID{}
			for id, r := range p.Repos {
				if o, dup := names[r.Name]; dup {
					t.Errorf("project %s: repos %s and %s share the name %q", pid, o, id, r.Name)
				}
				if o, dup := rels[r.RelPath]; dup {
					t.Errorf("project %s: repos %s and %s share the path %q", pid, o, id, r.RelPath)
				}
				names[r.Name], rels[r.RelPath] = id, id
			}
		}
	})
	st.need(t, "tracked")
}

func TestPropActiveRepoHasActiveProject(t *testing.T) { // T3
	st := forEachStep(t, func(t *testing.T, _, ws Workspace, _ Input, _ []Command, _ Outcome) {
		for pid, p := range ws.Projects {
			for id, r := range p.Repos {
				if isActive(r.Life) && !isActive(p.Life) {
					t.Errorf("repo %s is Active in the Untracked project %s", id, pid)
				}
			}
		}
	})
	st.need(t, "untracked")
}

func TestPropNoRowRemoved(t *testing.T) { // T4
	forEachStep(t, func(t *testing.T, before, after Workspace, _ Input, _ []Command, _ Outcome) {
		for pid, p := range before.Projects {
			q, ok := after.Projects[pid]
			if !ok {
				t.Errorf("project %s was removed", pid)
				continue
			}
			for id, r := range p.Repos {
				if r2, ok := q.Repos[id]; !ok {
					t.Errorf("repo %s was removed", id)
				} else if r2.RelPath != r.RelPath || r2.ID != r.ID {
					t.Errorf("repo %s changed its identity", id)
				}
			}
		}
	})
}

func TestPropBusyRepoStaysTracked(t *testing.T) { // T5
	st := forEachStep(t, func(t *testing.T, before, after Workspace, _ Input, _ []Command, _ Outcome) {
		for pid, p := range before.Projects {
			for id, r := range p.Repos {
				if _, busy := before.Busy[id]; busy && isActive(r.Life) && !isActive(after.Projects[pid].Repos[id].Life) {
					t.Errorf("busy repo %s was untracked", id)
				}
			}
		}
	})
	st.need(t, "busy")
}

func TestPropRestoreClearsTrust(t *testing.T) { // T6
	st := forEachStep(t, func(t *testing.T, before, after Workspace, _ Input, _ []Command, _ Outcome) {
		for pid, p := range after.Projects {
			for id, r := range p.Repos {
				old, existed := before.Projects[pid].Repos[id]
				if existed && !isActive(old.Life) && isActive(r.Life) && r.Trusted {
					t.Errorf("repo %s came back trusted", id)
				}
			}
		}
	})
	st.need(t, "restored")
}

func TestPropTrackIdempotent(t *testing.T) { // T7
	for seed := 1; seed <= seeds; seed++ {
		s := newSim(seed)
		for i := 0; i < 40; i++ {
			s.step()
			s.shell()
			in := s.trackProject()
			once, _, out := Decide(s.ws, in)
			switch out.(type) {
			case Tracked, AlreadyTracked:
			default:
				continue
			}
			again := in
			again.NewProjectID = core.ProjectID(s.fresh("prj"))
			again.Repos = append([]Pick(nil), in.Repos...)
			for j := range again.Repos {
				again.Repos[j].NewRepoID = core.RepoID(s.fresh("rep"))
			}
			twice, cmds, out2 := Decide(once, again)
			if len(cmds) != 0 || !reflect.DeepEqual(once, twice) {
				t.Fatalf("seed %d: tracking %#v twice changed the workspace or emitted %d commands (outcome %#v)", seed, in, len(cmds), out2)
			}
			if _, ok := out2.(AlreadyTracked); !ok {
				t.Fatalf("seed %d: second track gave %#v, want AlreadyTracked", seed, out2)
			}
		}
	}
}

func rowChanged(a, b interface{}) bool { return !reflect.DeepEqual(a, b) }

func TestPropAuditPerRowChange(t *testing.T) { // T8
	st := forEachStep(t, func(t *testing.T, before, after Workspace, _ Input, cmds []Command, _ Outcome) {
		audits := map[string]int{}
		for _, c := range cmds {
			if a, ok := c.(WriteAudit); ok {
				audits[a.Subject]++
			}
		}
		want := map[string]bool{}
		for pid, p := range after.Projects {
			old, existed := before.Projects[pid]
			if !existed || rowChanged(old.Life, p.Life) || old.Name != p.Name {
				want["project:"+string(pid)] = true
			}
			for id, r := range p.Repos {
				if o, ok := old.Repos[id]; !ok || rowChanged(o.Life, r.Life) {
					want["repo:"+string(id)] = true
				}
			}
		}
		for subject := range want {
			if audits[subject] != 1 {
				t.Errorf("%s changed and has %d audit entries, want 1", subject, audits[subject])
			}
		}
		for subject, n := range audits {
			if !want[subject] {
				t.Errorf("%s has %d audit entries but did not change", subject, n)
			}
		}
		if len(want) == 0 && len(cmds) != 0 {
			t.Errorf("nothing changed but %d commands were emitted", len(cmds))
		}
	})
	st.need(t, "tracked", "untracked", "already")
}

func TestPropRestoreKeepsID(t *testing.T) { // T9
	st := forEachStep(t, func(t *testing.T, before, after Workspace, in Input, _ []Command, out Outcome) {
		var proj Project
		var picks []Pick
		var found bool
		switch in := in.(type) {
		case TrackProject:
			proj, found = projectAt(before, in.Path)
			picks = in.Repos
		case TrackRepos:
			proj, found = before.Projects[in.ProjectID]
			picks = in.Repos
		}
		if !found {
			return
		}
		if _, ok := out.(Tracked); !ok {
			return
		}
		rows := repoByRel(proj)
		for _, pk := range picks {
			row, ok := rows[pk.RelPath]
			if !ok {
				continue
			}
			for _, p := range after.Projects {
				if _, dup := p.Repos[pk.NewRepoID]; dup {
					t.Errorf("pick %s got a new row %s although %s exists", pk.RelPath, pk.NewRepoID, row.ID)
				}
			}
			if _, ok := after.Projects[proj.ID].Repos[row.ID]; !ok {
				t.Errorf("the row %s of %s is gone", row.ID, pk.RelPath)
			}
		}
	})
	st.need(t, "restored")
}

func TestRunMatchesDecide(t *testing.T) {
	s := newSim(7)
	var ins []Input
	for i := 0; i < 30; i++ {
		ins = append(ins, s.input())
	}
	ws := Workspace{Projects: map[core.ProjectID]Project{}}
	var cmds []Command
	var outs []Outcome
	for _, in := range ins {
		var c []Command
		var o Outcome
		ws, c, o = Decide(ws, in)
		cmds, outs = append(cmds, c...), append(outs, o)
	}
	ws2, cmds2, outs2 := Run(Workspace{Projects: map[core.ProjectID]Project{}}, ins)
	if !reflect.DeepEqual(ws, ws2) || !reflect.DeepEqual(cmds, cmds2) || !reflect.DeepEqual(outs, outs2) {
		t.Error("Run differs from folding Decide")
	}
}
