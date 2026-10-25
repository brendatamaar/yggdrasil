package repos_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// perfEnv switches the performance test on. Starting git takes seconds on
// some Windows machines (antivirus, cold caches), so the test would flake in
// `task test`; `task perf` sets it.
const perfEnv = "YGG_PERF"

// perfBudget is the PRD section 8 limit: status refresh for 50 repos.
const perfBudget = 10 * time.Second

const perfRepoCount = 50

// countingGit counts and times the git calls the status reader makes.
type countingGit struct {
	g       *git.Git
	mu      sync.Mutex
	status  int
	log     int
	total   time.Duration
	longest time.Duration
	active  atomic.Int64
	peak    atomic.Int64
}

func (c *countingGit) enter() func(isLog bool) {
	n := c.active.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	start := time.Now()
	return func(isLog bool) {
		d := time.Since(start)
		c.active.Add(-1)
		c.mu.Lock()
		defer c.mu.Unlock()
		if isLog {
			c.log++
		} else {
			c.status++
		}
		c.total += d
		c.longest = max(c.longest, d)
	}
}

func (c *countingGit) Status(ctx context.Context, dir string) (git.Status, error) {
	done := c.enter()
	defer done(false)
	return c.g.Status(ctx, dir)
}

func (c *countingGit) Log(ctx context.Context, dir string, n int) ([]git.Commit, error) {
	done := c.enter()
	defer done(true)
	return c.g.Log(ctx, dir, n)
}

func (c *countingGit) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status + c.log
}

// copyTree copies a folder tree (files and folders only). A git repo has no
// absolute paths inside, so a copy is a working repo.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
}

// perfTemplate makes a repo with some tracked files. With upstream it also
// has origin/main one commit away on each side (1 ahead, 1 behind), made
// without any network.
func perfTemplate(f *gitFx, name string, upstream bool) string {
	dir := f.repo(name)
	for i := range 12 {
		sub := filepath.Join(dir, "src", fmt.Sprintf("pkg%d", i%3))
		if err := os.MkdirAll(sub, 0o750); err != nil {
			f.t.Fatal(err)
		}
		f.write(sub, fmt.Sprintf("file%d.txt", i), fmt.Sprintf("content %d\n", i))
	}
	f.run(dir, "add", "-A")
	f.run(dir, "commit", "-q", "-m", "init")
	if upstream {
		f.run(dir, "checkout", "-q", "-b", "up")
		f.commit(dir, "up.txt", "u\n", "teammate work")
		f.run(dir, "update-ref", "refs/remotes/origin/main", "up")
		f.run(dir, "checkout", "-q", "main")
		f.run(dir, "branch", "-D", "up")
		f.run(dir, "config", "remote.origin.url", "https://git.example.invalid/acme-shop/shop-api.git")
		f.run(dir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
		f.run(dir, "config", "branch.main.remote", "origin")
		f.run(dir, "config", "branch.main.merge", "refs/heads/main")
		f.commit(dir, "local.txt", "l\n", "local work")
	}
	return dir
}

// perfWant is what a repo's snapshot must show.
type perfWant struct{ ahead, behind, unstaged, untracked int }

// TestPerf_StatusRefresh50Repos is the PRD section 8 check: the real status
// service with the real git reader refreshes 50 repos in under 10 seconds,
// both at startup and on a user refresh. It only runs with YGG_PERF=1.
func TestPerf_StatusRefresh50Repos(t *testing.T) {
	if os.Getenv(perfEnv) == "" {
		t.Skipf("performance test: set %s=1 (task perf) to run it", perfEnv)
	}
	f := newGitFx(t)
	cleanT := perfTemplate(f, "tpl-clean", false)
	aheadT := perfTemplate(f, "tpl-ahead", true)

	root := t.TempDir()
	var rows []core.Repo
	want := map[core.RepoID]perfWant{}
	for i := range perfRepoCount {
		name := fmt.Sprintf("shop-%02d", i)
		dir := filepath.Join(root, name)
		var w perfWant
		switch i % 5 {
		case 0, 1: // clean
			copyTree(t, cleanT, dir)
		case 2: // dirty
			copyTree(t, cleanT, dir)
			f.write(filepath.Join(dir, "src", "pkg0"), "file0.txt", "changed\n")
			f.write(dir, "scratch.txt", "x\n")
			w = perfWant{unstaged: 1, untracked: 1}
		case 3: // ahead and behind
			copyTree(t, aheadT, dir)
			w = perfWant{ahead: 1, behind: 1}
		case 4: // ahead, behind and dirty
			copyTree(t, aheadT, dir)
			f.write(dir, "scratch.txt", "x\n")
			w = perfWant{ahead: 1, behind: 1, untracked: 1}
		}
		id := core.RepoID("rep_" + name)
		rows = append(rows, core.Repo{ID: id, ProjectID: "prj_1", Name: name, Path: dir})
		want[id] = w
	}

	cg := &countingGit{g: f.g}
	src := &memSource{}
	src.set(rows...)
	svc := repos.NewStatusService(src, repos.NewGitReader(cg), time.Now, nil)
	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	began := time.Now()
	g.Go(func() error { return svc.Start(gctx) })
	t.Cleanup(func() {
		cancel()
		if err := g.Wait(); err != nil {
			t.Errorf("Start returned %v", err)
		}
	})

	// waitAll returns the time since `since` once every repo has a Fresh view
	// read at or after `since` and no read is queued or in flight.
	waitAll := func(label string, since time.Time) time.Duration {
		for {
			s, err := svc.Snapshot(ctx)
			if err == nil && len(s.Entries) == perfRepoCount && len(s.InFlight) == 0 && len(s.Queue) == 0 {
				ok := true
				for _, e := range s.Entries {
					fr, isFresh := e.View.(corerepos.Fresh)
					if !isFresh || fr.At.Before(since) {
						ok = false
						break
					}
				}
				if ok {
					return time.Since(since)
				}
			}
			if time.Since(since) > 3*perfBudget {
				t.Fatalf("%s: not finished after %v", label, time.Since(since))
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	startup := waitAll("startup refresh", began)
	startCalls := cg.calls()

	s, err := svc.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, w := range want {
		fr, ok := s.Entries[id].View.(corerepos.Fresh)
		if !ok {
			t.Fatalf("%s: view is %T, want Fresh", id, s.Entries[id].View)
		}
		got := perfWant{fr.Snap.Ahead, fr.Snap.Behind, fr.Snap.Unstaged, fr.Snap.Untracked}
		if got != w {
			t.Errorf("%s: snapshot counts = %+v, want %+v", id, got, w)
		}
		if fr.Snap.LastCommit == nil {
			t.Errorf("%s: no last commit", id)
		}
	}

	begin := time.Now()
	if err := svc.Refresh(ctx, nil, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	user := waitAll("user refresh", begin)

	cg.mu.Lock()
	t.Logf("PERF 50 repos, max reads %d (peak seen %d): startup %v, user refresh %v (budget %v)",
		corerepos.DefaultMax, cg.peak.Load(), startup.Round(time.Millisecond), user.Round(time.Millisecond), perfBudget)
	t.Logf("PERF git calls: startup %d (%.1f per repo); total %d = %d status + %d log; mean %v, longest %v per call",
		startCalls, float64(startCalls)/perfRepoCount, cg.status+cg.log, cg.status, cg.log,
		(cg.total / time.Duration(max(cg.status+cg.log, 1))).Round(time.Millisecond), cg.longest.Round(time.Millisecond))
	cg.mu.Unlock()

	if startup >= perfBudget {
		t.Errorf("startup refresh of %d repos took %v, want under %v", perfRepoCount, startup, perfBudget)
	}
	if user >= perfBudget {
		t.Errorf("user refresh of %d repos took %v, want under %v", perfRepoCount, user, perfBudget)
	}
}
