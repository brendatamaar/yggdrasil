package standup_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/standup"
	"github.com/brendatamaar/yggdrasil/internal/standup/standuptest"
)

// The real merge request source has the cached host data as its only input;
// the real build source reads a real temporary database.
func TestRealSourceContract(t *testing.T) {
	t.Run("cached merge requests", func(t *testing.T) { standuptest.SourceContract(t, newMRSource) })
	t.Run("database builds", func(t *testing.T) { standuptest.SourceContract(t, newBuildSource) })
}

// *hosts.Service is the production HostCache: the poller's in-memory data.
var _ standup.HostCache = (*hosts.Service)(nil)

// --- cached merge requests ---

const inst corehosts.InstanceID = "gitlab_main"

// cacheFake is a HostCache with scripted content. It also has methods a real
// service has and the source must never use (Refresh), and counts them.
type cacheFake struct {
	mu      sync.Mutex
	repos   map[core.RepoID]*cachedRepo
	inbox   []api.InboxEntry
	refresh int
	reads   int
}

type cachedRepo struct {
	project corehosts.ProjectPath
	entry   corehosts.Entry
	linked  bool
	reason  string
}

func newCache() *cacheFake { return &cacheFake{repos: map[core.RepoID]*cachedRepo{}} }

func (c *cacheFake) RepoHost(_ context.Context, repo core.RepoID) (api.RepoHostView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	r, ok := c.repos[repo]
	if !ok {
		return api.RepoHostView{NotLinked: "not looked at yet"}, nil
	}
	if !r.linked {
		return api.RepoHostView{NotLinked: r.reason}, nil
	}
	return api.RepoHostView{Linked: true, Instance: corehosts.Instance{ID: inst}, Project: r.project, Entry: r.entry}, nil
}

func (c *cacheFake) Inbox(context.Context) (api.InboxView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	return api.InboxView{Items: append([]api.InboxEntry(nil), c.inbox...)}, nil
}

// Refresh stands for every network-capable method of the real service.
func (c *cacheFake) Refresh(context.Context, []corehosts.Key) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refresh++
	return nil
}

func (c *cacheFake) link(repo core.RepoID, own *corehosts.MergeRequest) corehosts.ProjectPath {
	path := corehosts.ProjectPath("acme/" + string(repo))
	var data corehosts.Payload = corehosts.RepoState{MR: own}
	c.repos[repo] = &cachedRepo{project: path, linked: true, entry: corehosts.Entry{
		Phase: corehosts.Idle{Data: data, FetchedAt: standuptest.Base},
	}}
	return path
}

func (c *cacheFake) addInbox(path corehosts.ProjectPath, m corehosts.MergeRequest) {
	c.inbox = append(c.inbox, api.InboxEntry{Instance: inst, Item: corehosts.InboxItem{MR: m, Repo: string(path), Role: corehosts.RoleAssigned}})
}

func hostMR(n int, title string, at time.Time) corehosts.MergeRequest {
	return corehosts.MergeRequest{Number: n, Title: title, URL: fmt.Sprintf("https://git.example.test/acme/x/-/merge_requests/%d", n),
		Author: "ada", Source: "feature", Target: "main", State: corehosts.MROpen, UpdatedAt: at}
}

type mrSetup struct{ c *cacheFake }

// Seed puts the first fact in the repo's own branch entry and the rest in the
// inbox, so both cache paths are read.
func (s mrSetup) Seed(_ *testing.T, repo core.RepoID, fs []standuptest.Fact) {
	var own *corehosts.MergeRequest
	if len(fs) > 0 {
		m := hostMR(1, fs[0].Subject, fs[0].At)
		own = &m
	}
	path := s.c.link(repo, own)
	for i := 1; i < len(fs); i++ {
		s.c.addInbox(path, hostMR(i+1, fs[i].Subject, fs[i].At))
	}
}
func (mrSetup) Missing() core.RepoID { return "rep_gone" }

func newMRSource(t *testing.T) (standuptest.Reader, standuptest.SourceSetup) {
	t.Helper()
	c := newCache()
	src, err := standup.NewMRSource(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mrReader(src), mrSetup{c}
}

func mrReader(src standup.MRSource) standuptest.Reader {
	return func(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]standuptest.Fact, bool, error) {
		ms, trunc, err := src.MRs(ctx, repo, from, to, max)
		out := make([]standuptest.Fact, 0, len(ms))
		for _, m := range ms {
			if m.Repo != repo {
				return nil, false, fmt.Errorf("record of repo %s returned for %s", m.Repo, repo)
			}
			out = append(out, standuptest.Fact{At: m.At, Subject: m.Title})
		}
		return out, trunc, err
	}
}

func TestMRSourceReadsOnlyTheCache(t *testing.T) {
	c := newCache()
	path := c.link("rep_api", nil)
	c.addInbox(path, hostMR(7, "Add paging", standuptest.Base))
	src, err := standup.NewMRSource(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := src.MRs(context.Background(), "rep_api", standuptest.Base, standuptest.Base.Add(time.Hour), 200); err != nil {
			t.Fatal(err)
		}
	}
	if c.refresh != 0 {
		t.Fatalf("a read called Refresh %d times: a standup refresh must not reach the network", c.refresh)
	}
	if c.reads == 0 {
		t.Fatal("the source did not read the cache")
	}
}

// The source file imports nothing that can reach a network, a process or a
// token: the narrow HostCache is its only way to host data.
func TestMRSourceImportsNoNetworkOrHostPackage(t *testing.T) {
	src, err := os.ReadFile("mrs.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"net/http"`, `"net"`, `"os/exec"`, `internal/hosts"`, `internal/platform"`, `internal/hosts/`} {
		if strings.Contains(string(src), banned) {
			t.Errorf("mrs.go imports %s", banned)
		}
	}
	for _, banned := range []string{"Refresh(", "Token", "token"} {
		code := string(src)
		// Comments may say what the source does not do; only code counts.
		for _, line := range strings.Split(code, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "//") {
				continue
			}
			if strings.Contains(line, banned) {
				t.Errorf("mrs.go code mentions %q: %s", banned, line)
			}
		}
	}
}

func TestMRSourceLabelsWhatItCannotRead(t *testing.T) {
	ctx := context.Background()
	c := newCache()
	src, _ := standup.NewMRSource(c, nil)
	from, to := standuptest.Base, standuptest.Base.Add(time.Hour)

	// Never looked at, and linked to nothing: an error, never an empty success.
	c.repos["rep_nolink"] = &cachedRepo{reason: "no remote"}
	for _, repo := range []core.RepoID{"rep_unknown", "rep_nolink"} {
		if ms, _, err := src.MRs(ctx, repo, from, to, 10); !errors.Is(err, standup.ErrMRNotLinked) || len(ms) != 0 {
			t.Errorf("%s: %v, %v; want ErrMRNotLinked", repo, ms, err)
		}
	}
	// Linked but the poller has not fetched yet.
	c.repos["rep_new"] = &cachedRepo{project: "acme/new", linked: true, entry: corehosts.Entry{Phase: corehosts.Idle{}}}
	if _, _, err := src.MRs(ctx, "rep_new", from, to, 10); !errors.Is(err, standup.ErrMRNotCached) {
		t.Errorf("not fetched: %v; want ErrMRNotCached", err)
	}
	// Blocked or waiting keeps its last data readable.
	c.repos["rep_blocked"] = &cachedRepo{project: "acme/blocked", linked: true, entry: corehosts.Entry{Phase: corehosts.Blocked{
		Why: corehosts.BlockUnauthorized, Prev: corehosts.Idle{FetchedAt: from, Data: corehosts.RepoState{MR: ptr(hostMR(3, "Old", from))}},
	}}}
	if ms, _, err := src.MRs(ctx, "rep_blocked", from, to, 10); err != nil || len(ms) != 1 {
		t.Errorf("blocked with data: %v, %v", ms, err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestMRSourceBoundary(t *testing.T) {
	ctx := context.Background()
	from, to := standuptest.Base, standuptest.Base.Add(time.Hour)
	type tc struct {
		name string
		mod  func(*corehosts.MergeRequest)
	}
	for _, c := range []tc{
		{"no number", func(m *corehosts.MergeRequest) { m.Number = 0 }},
		{"negative number", func(m *corehosts.MergeRequest) { m.Number = -2 }},
		{"unknown state", func(m *corehosts.MergeRequest) { m.State = "wip" }},
		{"no update time", func(m *corehosts.MergeRequest) { m.UpdatedAt = time.Time{} }},
		{"empty title", func(m *corehosts.MergeRequest) { m.Title = " \t" }},
	} {
		t.Run("rejects "+c.name, func(t *testing.T) {
			cache := newCache()
			m := hostMR(4, "Fix cart", from.Add(time.Minute))
			c.mod(&m)
			cache.addInbox(cache.link("rep_api", nil), m)
			src, _ := standup.NewMRSource(cache, nil)
			ms, _, err := src.MRs(ctx, "rep_api", from, to, 10)
			if err == nil || len(ms) != 0 {
				t.Fatalf("got %v, %v; want an error and no data", ms, err)
			}
		})
	}
	t.Run("rejects bad arguments", func(t *testing.T) {
		src, _ := standup.NewMRSource(newCache(), nil)
		for _, a := range []struct {
			repo     core.RepoID
			from, to time.Time
			max      int
		}{{"", from, to, 5}, {"rep_a", to, from, 5}, {"rep_a", from, from, 5}, {"rep_a", from, to, 0}} {
			if _, _, err := src.MRs(ctx, a.repo, a.from, a.to, a.max); err == nil {
				t.Errorf("%+v accepted", a)
			}
		}
	})
	t.Run("redacts titles", func(t *testing.T) {
		cache := newCache()
		const secret = "glpat-abcdefghijklmnopqrst"
		cache.addInbox(cache.link("rep_api", nil), hostMR(5, "Use "+secret+" for CI", from.Add(time.Minute)))
		src, _ := standup.NewMRSource(cache, nil)
		ms, _, err := src.MRs(ctx, "rep_api", from, to, 10)
		if err != nil || len(ms) != 1 {
			t.Fatal(ms, err)
		}
		if strings.Contains(ms[0].Title, secret) {
			t.Fatalf("token reached the title: %q", ms[0].Title)
		}
	})
	t.Run("redacts a token split by an invalid byte", func(t *testing.T) {
		cache := newCache()
		const secret = "glpat-abcdefghijklmnopqrst"
		split := secret[:10] + "\xff" + secret[10:]
		cache.addInbox(cache.link("rep_api", nil), hostMR(5, "Use "+split+" for CI", from.Add(time.Minute)))
		src, _ := standup.NewMRSource(cache, safety.NewRedactor(secret))
		ms, _, err := src.MRs(ctx, "rep_api", from, to, 10)
		if err != nil || len(ms) != 1 {
			t.Fatal(ms, err)
		}
		if strings.Contains(ms[0].Title, secret[:10]) {
			t.Fatalf("a split token survived: %q", ms[0].Title)
		}
	})
	t.Run("does not leak another repo or instance", func(t *testing.T) {
		cache := newCache()
		a := cache.link("rep_api", nil)
		b := cache.link("rep_web", nil)
		cache.addInbox(a, hostMR(1, "api change", from.Add(time.Minute)))
		cache.addInbox(b, hostMR(2, "web change", from.Add(time.Minute)))
		cache.inbox = append(cache.inbox, api.InboxEntry{Instance: "other_host", Item: corehosts.InboxItem{MR: hostMR(3, "other host", from.Add(time.Minute)), Repo: string(a)}})
		src, _ := standup.NewMRSource(cache, nil)
		ms, _, err := src.MRs(ctx, "rep_api", from, to, 10)
		if err != nil || len(ms) != 1 || ms[0].Title != "api change" {
			t.Fatalf("got %v, %v", ms, err)
		}
	})
	t.Run("one merge request seen twice appears once, newest update", func(t *testing.T) {
		cache := newCache()
		own := hostMR(9, "Same MR", from.Add(10*time.Minute))
		path := cache.link("rep_api", &own)
		cache.addInbox(path, hostMR(9, "Same MR", from.Add(20*time.Minute)))
		src, _ := standup.NewMRSource(cache, nil)
		ms, _, err := src.MRs(ctx, "rep_api", from, to, 10)
		if err != nil || len(ms) != 1 || !ms[0].At.Equal(from.Add(20*time.Minute)) {
			t.Fatalf("got %v, %v", ms, err)
		}
	})
	t.Run("order is newest first, ties by number", func(t *testing.T) {
		cache := newCache()
		path := cache.link("rep_api", nil)
		at := from.Add(time.Minute)
		for _, n := range []int{5, 2, 9} {
			cache.addInbox(path, hostMR(n, fmt.Sprintf("mr %d", n), at))
		}
		cache.addInbox(path, hostMR(1, "newer", at.Add(time.Minute)))
		src, _ := standup.NewMRSource(cache, nil)
		ms, _, err := src.MRs(ctx, "rep_api", from, to, 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []int
		for _, m := range ms {
			got = append(got, m.Number)
		}
		if fmt.Sprint(got) != "[1 2 5 9]" {
			t.Fatalf("order %v, want [1 2 5 9]", got)
		}
	})
}

// --- database builds ---

type buildSetup struct {
	st   *db.Store
	proj core.Project
	// ids maps a stored build ID back to the fact subject it was seeded from.
	mu  sync.Mutex
	ids map[core.BuildID]string
}

func newBuildSource(t *testing.T) (standuptest.Reader, standuptest.SourceSetup) {
	t.Helper()
	s := newBuildSetup(t)
	src, err := standup.NewBuildSource(s.st)
	if err != nil {
		t.Fatal(err)
	}
	return s.reader(src), s
}

func newBuildSetup(t *testing.T) *buildSetup {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := db.NewStore(d, nil)
	p := dbtest.Project(t, st, core.Project{Name: "acme-shop", Path: filepath.Join(t.TempDir(), "acme-shop")})
	return &buildSetup{st: st, proj: p, ids: map[core.BuildID]string{}}
}

func (s *buildSetup) reader(src standup.BuildSource) standuptest.Reader {
	return func(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]standuptest.Fact, bool, error) {
		bs, trunc, err := src.Builds(ctx, repo, from, to, max)
		out := make([]standuptest.Fact, 0, len(bs))
		for _, b := range bs {
			if b.Repo != repo {
				return nil, false, fmt.Errorf("record of repo %s returned for %s", b.Repo, repo)
			}
			s.mu.Lock()
			subject := s.ids[b.ID]
			s.mu.Unlock()
			out = append(out, standuptest.Fact{At: b.At, Subject: subject})
		}
		return out, trunc, err
	}
}

func (s *buildSetup) addRepo(t *testing.T, repo core.RepoID) core.Repo {
	t.Helper()
	dir := filepath.Join(s.proj.Path, string(repo))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dbtest.Repo(t, s.st, core.Repo{ID: repo, ProjectID: s.proj.ID, Name: string(repo), Path: dir, DefaultBranch: "main"})
}

func spec(repos ...core.RepoID) corebuilds.Spec {
	sp := corebuilds.Spec{Target: corebuilds.MainCheckout{}}
	for _, r := range repos {
		sp.Steps = append(sp.Steps, corebuilds.StepSpec{RepoID: r, Name: "verify", Argv: []string{"tool", "verify"}})
	}
	return sp
}

func (s *buildSetup) save(t *testing.T, st corebuilds.State) {
	t.Helper()
	err := s.st.Commit(context.Background(), func(tx *db.Tx) error {
		_, err := tx.SaveBuild(context.Background(), st, db.NewBuild)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Seed stores one running build per fact, started at the fact's instant.
func (s *buildSetup) Seed(t *testing.T, repo core.RepoID, fs []standuptest.Fact) {
	t.Helper()
	s.addRepo(t, repo)
	for i, f := range fs {
		id := core.BuildID(fmt.Sprintf("bld_%s_%02d", repo, i))
		s.mu.Lock()
		s.ids[id] = f.Subject
		s.mu.Unlock()
		s.save(t, running(id, spec(repo), f.At))
	}
}

func (s *buildSetup) Missing() core.RepoID { return "rep_gone" }

func running(id core.BuildID, sp corebuilds.Spec, started time.Time) corebuilds.Running {
	steps := make([]corebuilds.StepState, len(sp.Steps))
	for i := range steps {
		steps[i] = corebuilds.StepPending{}
	}
	steps[0] = corebuilds.StepRunning{Attempt: 1, StartedAt: started}
	return corebuilds.Running{ID: id, Spec: sp, QueuedAt: started.Add(-time.Second), StartedAt: started, Actor: "user", Steps: steps}
}

func finished(id core.BuildID, sp corebuilds.Spec, queued time.Time, started *time.Time, end time.Time, res corebuilds.Result) corebuilds.Finished {
	steps := make([]corebuilds.StepState, len(sp.Steps))
	for i := range steps {
		steps[i] = corebuilds.StepSkipped{}
	}
	return corebuilds.Finished{ID: id, Spec: sp, QueuedAt: queued, Actor: "user", StartedAt: started, FinishedAt: end, Steps: steps, Result: res}
}

func TestBuildSourceTimes(t *testing.T) {
	ctx := context.Background()
	s := newBuildSetup(t)
	s.addRepo(t, "rep_api")
	s.addRepo(t, "rep_web")
	b := standuptest.Base
	at := func(m int) time.Time { return b.Add(time.Duration(m) * time.Minute) }
	sp := spec("rep_api")
	cancelled := corebuilds.ResultCancelled{}

	// queued only: neither a start nor a finish time, never listed.
	s.save(t, corebuilds.Queued{ID: "bld_queued", Spec: sp, QueuedAt: at(5), Actor: "user"})
	// started before the window, finished inside: listed at the finish.
	st := at(-30)
	fin := finished("bld_finish_in", sp, at(-31), &st, at(5), corebuilds.ResultSucceeded{})
	fin.Steps = []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, StartedAt: st, FinishedAt: at(5), Outcome: corebuilds.Succeeded{}}}
	s.save(t, fin)
	// started inside, finished after the window: listed at the start.
	st2 := at(6)
	fin2 := finished("bld_start_in", sp, at(5), &st2, at(500), corebuilds.ResultSucceeded{})
	fin2.Steps = []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, StartedAt: st2, FinishedAt: at(500), Outcome: corebuilds.Succeeded{}}}
	s.save(t, fin2)
	// cancelled while queued: no start time, finished inside.
	s.save(t, withCancel(finished("bld_cancelled_queued", sp, at(1), nil, at(7), cancelled), at(7)))
	// outside on both ends, and another repo.
	st3 := at(-60)
	fin3 := finished("bld_outside", sp, at(-61), &st3, at(-50), corebuilds.ResultSucceeded{})
	fin3.Steps = []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, StartedAt: st3, FinishedAt: at(-50), Outcome: corebuilds.Succeeded{}}}
	s.save(t, fin3)
	s.save(t, running("bld_other_repo", spec("rep_web"), at(8)))
	// running started exactly at the end instant: excluded (half-open).
	s.save(t, running("bld_at_end", sp, at(10)))
	// running started exactly at the start instant: included.
	s.save(t, running("bld_at_start", sp, at(0)))

	src, err := standup.NewBuildSource(s.st)
	if err != nil {
		t.Fatal(err)
	}
	got, trunc, err := src.Builds(ctx, "rep_api", at(0), at(10), 50)
	if err != nil || trunc {
		t.Fatal(trunc, err)
	}
	var ids []string
	for _, x := range got {
		ids = append(ids, string(x.ID))
	}
	want := "[bld_cancelled_queued bld_start_in bld_finish_in bld_at_start]"
	if fmt.Sprint(ids) != want {
		t.Fatalf("got %v, want %s", ids, want)
	}
	for _, x := range got {
		if x.RepoName != "rep_api" || x.Status.Validate() != nil {
			t.Errorf("bad record %+v", x)
		}
	}
	if got[0].Status != core.BuildCancelled || got[2].Status != core.BuildSucceeded || got[3].Status != core.BuildRunning {
		t.Errorf("statuses %v %v %v", got[0].Status, got[2].Status, got[3].Status)
	}
}

func withCancel(f corebuilds.Finished, at time.Time) corebuilds.Finished {
	f.CancelledAt = &at
	return f
}

func TestBuildSourceTiesByIDAndUntrackedRepo(t *testing.T) {
	ctx := context.Background()
	s := newBuildSetup(t)
	repo := s.addRepo(t, "rep_api")
	for _, id := range []core.BuildID{"bld_c", "bld_a", "bld_b"} {
		s.save(t, running(id, spec("rep_api"), standuptest.Base.Add(time.Minute)))
	}
	src, _ := standup.NewBuildSource(s.st)
	got, trunc, err := src.Builds(ctx, "rep_api", standuptest.Base, standuptest.Base.Add(time.Hour), 2)
	if err != nil || !trunc || len(got) != 2 || got[0].ID != "bld_a" || got[1].ID != "bld_b" {
		t.Fatalf("got %+v, truncated %v, err %v", got, trunc, err)
	}
	dbtest.UntrackRepo(t, s.st, repo)
	if got, _, err := src.Builds(ctx, "rep_api", standuptest.Base, standuptest.Base.Add(time.Hour), 2); err == nil || len(got) != 0 {
		t.Fatalf("untracked repo: %v, %v; want an error", got, err)
	}
}
