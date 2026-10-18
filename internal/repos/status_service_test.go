package repos_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
)

// memSource is an in-memory ActiveSource.
type memSource struct {
	mu    sync.Mutex
	repos []core.Repo
}

func (m *memSource) set(rs ...core.Repo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repos = rs
}

func (m *memSource) ListProjects(context.Context) ([]core.Project, error) {
	return []core.Project{{ID: "prj_1", Name: "acme-shop"}}, nil
}

func (m *memSource) ListRepos(context.Context, core.ProjectID) ([]core.Repo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]core.Repo(nil), m.repos...), nil
}

func repoRow(name string) core.Repo {
	return core.Repo{ID: core.RepoID("rep_" + name), ProjectID: "prj_1", Name: name, Path: "/work/" + name}
}

func cpath(name string) corerepos.CleanPath { return corerepos.CleanPath("/work/" + name) }

// recorder is a Publisher that records what it is told.
type recorder struct {
	mu     sync.Mutex
	repos  []core.RepoID
	counts int
}

func (r *recorder) RepoChanged(id core.RepoID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repos = append(r.repos, id)
}

func (r *recorder) CountsChanged() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts++
}

func (r *recorder) seen() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.repos), r.counts
}

type harness struct {
	svc    *repos.StatusService
	src    *memSource
	reader *repostest.FakeReader
	pub    *recorder
	cancel context.CancelFunc
	done   chan error
}

// start runs a service over the named repos, each scripted as a clean repo.
func start(t *testing.T, names []string, clock func() time.Time, opts ...repos.StatusOption) *harness {
	t.Helper()
	h := &harness{src: &memSource{}, reader: repostest.NewFakeReader(), pub: &recorder{}, done: make(chan error, 1)}
	var rows []core.Repo
	for _, n := range names {
		rows = append(rows, repoRow(n))
		h.reader.Script(cpath(n), repostest.FakeResult{Snap: corerepos.Snapshot{Branch: "main", HasUpstream: true}})
	}
	h.src.set(rows...)
	if clock == nil {
		clock = func() time.Time { return time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC) }
	}
	h.svc = repos.NewStatusService(h.src, h.reader, clock, h.pub, opts...)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return h.svc.Start(gctx) })
	go func() { h.done <- g.Wait() }() // ends when the service ends; stop reads the result
	t.Cleanup(func() { h.stop(t) })
	eventually(t, "the service to run", func() bool {
		_, err := h.svc.Snapshot(context.Background())
		return err == nil
	})
	return h
}

func (h *harness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("Start returned %v", err)
		}
		h.done <- nil
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not stop")
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) snapshot(t *testing.T) corerepos.Refresher {
	t.Helper()
	s, err := h.svc.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *harness) allView(t *testing.T, n int, kind func(corerepos.View) bool) func() bool {
	return func() bool {
		s := h.snapshot(t)
		if len(s.Entries) != n || len(s.InFlight) != 0 || len(s.Queue) != 0 {
			return false
		}
		for _, e := range s.Entries {
			if !kind(e.View) {
				return false
			}
		}
		return true
	}
}

func isFresh(v corerepos.View) bool { _, ok := v.(corerepos.Fresh); return ok }

func TestStatusServiceReadsEveryRepoAtStartup(t *testing.T) {
	h := start(t, []string{"api", "web", "admin"}, nil)
	eventually(t, "all repos fresh", h.allView(t, 3, isFresh))
	eventually(t, "counts published", func() bool { _, c := h.pub.seen(); return c > 0 })
	if n, _ := h.pub.seen(); n == 0 {
		t.Error("no repo change was published")
	}
	for _, n := range []string{"api", "web", "admin"} {
		if c := h.reader.Calls(cpath(n)); c != 1 {
			t.Errorf("%s read %d times, want 1", n, c)
		}
	}
}

func TestStatusServiceBoundsConcurrentReads(t *testing.T) {
	var names []string
	for i := range 14 {
		names = append(names, fmt.Sprintf("r%d", i))
	}
	h := start(t, names, nil, repos.WithMaxReads(3))
	for _, n := range names {
		h.reader.Script(cpath(n), repostest.FakeResult{Snap: corerepos.Snapshot{Branch: "main"}, Delay: 20 * time.Millisecond})
	}
	// The scripts above may race with the first reads; ask again so every repo takes the delay.
	if err := h.svc.Refresh(context.Background(), nil, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "all repos fresh", h.allView(t, 14, isFresh))
	if got := h.reader.MaxActive(); got > 3 || got < 2 {
		t.Errorf("MaxActive = %d, want 2 or 3", got)
	}
}

func TestStatusServiceReadTimeoutIsAProblem(t *testing.T) {
	h := start(t, []string{"slow"}, nil, repos.WithReadTimeout(30*time.Millisecond))
	h.reader.Script(cpath("slow"), repostest.FakeResult{Delay: time.Minute})
	if err := h.svc.Refresh(context.Background(), nil, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "timed out", func() bool {
		p, ok := h.snapshot(t).Entries["rep_slow"].View.(corerepos.Problem)
		return ok && p.Kind == corerepos.ProblemTimedOut
	})
}

func TestStatusServiceFailureKeepsLastGoodSnapshot(t *testing.T) {
	h := start(t, []string{"api"}, nil)
	eventually(t, "fresh", h.allView(t, 1, isFresh))
	h.reader.Script(cpath("api"), repostest.FakeResult{Kind: corerepos.ProblemGitFailed, Err: fmt.Errorf("index is corrupt")})
	if err := h.svc.Refresh(context.Background(), []core.RepoID{"rep_api"}, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "problem", func() bool {
		p, ok := h.snapshot(t).Entries["rep_api"].View.(corerepos.Problem)
		return ok && p.Kind == corerepos.ProblemGitFailed && p.Last != nil && p.Last.Branch == "main"
	})
}

func TestStatusServiceRefreshAndTick(t *testing.T) {
	var offset atomic.Int64
	clock := func() time.Time {
		return time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC).Add(time.Duration(offset.Add(1)) * 5 * time.Minute)
	}
	h := start(t, []string{"api"}, clock, repos.WithTickEvery(15*time.Millisecond))
	// Each clock read is 5 minutes later, so every tick finds the view older than MinAge.
	eventually(t, "tick re-reads", func() bool { return h.reader.Calls(cpath("api")) >= 3 })
}

func TestStatusServiceUserRefreshReadsAgain(t *testing.T) {
	h := start(t, []string{"api", "web"}, nil)
	eventually(t, "fresh", h.allView(t, 2, isFresh))
	if err := h.svc.Refresh(context.Background(), []core.RepoID{"rep_web"}, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second read", func() bool { return h.reader.Calls(cpath("web")) == 2 })
	if c := h.reader.Calls(cpath("api")); c != 1 {
		t.Errorf("api read %d times, want 1", c)
	}
}

func TestStatusServiceFocusRespectsMinAge(t *testing.T) {
	h := start(t, []string{"api"}, nil) // the clock is fixed: the view is 0 s old
	eventually(t, "fresh", h.allView(t, 1, isFresh))
	if err := h.svc.Refresh(context.Background(), nil, corerepos.ReasonFocus); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Snapshot(context.Background()); err != nil { // a round trip through the owner
		t.Fatal(err)
	}
	if c := h.reader.Calls(cpath("api")); c != 1 {
		t.Errorf("focus refresh of a fresh view read again (%d reads)", c)
	}
}

func TestStatusServiceReloadReposAddsAndDrops(t *testing.T) {
	h := start(t, []string{"api"}, nil)
	eventually(t, "fresh", h.allView(t, 1, isFresh))
	h.reader.Script(cpath("web"), repostest.FakeResult{Snap: corerepos.Snapshot{Branch: "dev"}})
	h.src.set(repoRow("web"))
	if err := h.svc.ReloadRepos(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "web fresh and api gone", func() bool {
		s := h.snapshot(t)
		_, hasAPI := s.Entries["rep_api"]
		f, ok := s.Entries["rep_web"].View.(corerepos.Fresh)
		return !hasAPI && ok && f.Snap.Branch == "dev"
	})
}

func TestStatusServiceNotRunning(t *testing.T) {
	svc := repos.NewStatusService(&memSource{}, repostest.NewFakeReader(), time.Now, nil)
	_, err := svc.Snapshot(context.Background())
	if class, ok := apperr.ClassOf(err); !ok || class != apperr.Unavailable {
		t.Errorf("Snapshot before Start: %v, want an Unavailable error", err)
	}
	if err := svc.Refresh(context.Background(), nil, corerepos.ReasonUser); err == nil {
		t.Error("Refresh before Start succeeded")
	}
	if err := svc.Refresh(context.Background(), nil, corerepos.RefreshReason("sometime")); err == nil {
		t.Error("an unknown reason was accepted")
	}
}

func TestStatusServiceStopsWithItsContext(t *testing.T) {
	h := start(t, []string{"api", "web"}, nil)
	eventually(t, "fresh", h.allView(t, 2, isFresh))
	h.stop(t)
	_, err := h.svc.Snapshot(context.Background())
	if class, ok := apperr.ClassOf(err); !ok || class != apperr.Unavailable {
		t.Errorf("Snapshot after stop: %v, want an Unavailable error", err)
	}
}

func TestStatusServiceSkipsRepoWithBadPath(t *testing.T) {
	h := start(t, []string{"api"}, nil)
	bad := core.Repo{ID: "rep_bad", ProjectID: "prj_1", Name: "bad", Path: "relative/path"}
	h.src.set(repoRow("api"), bad)
	if err := h.svc.ReloadRepos(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := h.snapshot(t)
	if _, ok := s.Entries["rep_bad"]; ok || len(s.Entries) != 1 {
		t.Errorf("entries = %v, want only api", s.Entries)
	}
}

// Fifty real repos, read with Max 8. The formal performance test is B4; this
// logs the time so a regression shows up early. It needs git, so -short skips it.
func TestStatusServiceFiftyRealRepos(t *testing.T) {
	if testing.Short() || os.Getenv("YGG_PERF") == "" {
		t.Skip("slow: starts git for 50 repos; set YGG_PERF=1 to run it")
	}
	fx := newGitFx(t)
	base := t.TempDir()
	src := &memSource{}
	rows := make([]core.Repo, 50)
	var setup errgroup.Group
	setup.SetLimit(8)
	for i := range rows {
		dir := filepath.Join(base, fmt.Sprintf("repo%02d", i))
		rows[i] = core.Repo{ID: core.RepoID(fmt.Sprintf("rep_%02d", i)), ProjectID: "prj_1", Name: fmt.Sprintf("repo%02d", i), Path: dir}
		setup.Go(func() error {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return err
			}
			return fx.runErr(dir, "init", "-q", "-b", "main")
		})
	}
	if err := setup.Wait(); err != nil {
		t.Fatal(err)
	}
	src.set(rows...)
	svc := repos.NewStatusService(src, repos.NewGitReader(fx.g), func() time.Time { return time.Now().UTC() }, nil, repos.WithMaxReads(8))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	begin := time.Now()
	go func() { done <- svc.Start(ctx) }()
	for {
		s, err := svc.Snapshot(ctx)
		if err == nil && len(s.Entries) == 50 && len(s.InFlight) == 0 && len(s.Queue) == 0 {
			fresh := 0
			for _, e := range s.Entries {
				if isFresh(e.View) {
					fresh++
				}
			}
			if fresh != 50 {
				t.Fatalf("%d of 50 repos fresh", fresh)
			}
			break
		}
		if time.Since(begin) > 3*time.Minute {
			t.Fatal("50 repos not read in 3 minutes")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("status refresh of 50 repos (Max 8): %s", time.Since(begin).Round(time.Millisecond))
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
