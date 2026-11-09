package devservers_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
)

// Concurrent misses for one target run the detector once.
func TestCatalogConcurrentMissesScanOnce(t *testing.T) {
	repos, dir := devserverstest.NewRepos(), t.TempDir()
	target := devservers.Target{Repo: "rep_1"}
	repos.Track(target, dir)
	var calls atomic.Int32
	release := make(chan struct{})
	cat := devservers.NewCatalog(repos, devserverstest.NewClock(), devservers.WithDetector(func(string) []devservers.DevCommand {
		calls.Add(1)
		<-release // hold the scan open so every caller arrives during it
		return []devservers.DevCommand{{Key: "dev", Argv: []string{"npm", "run", "dev"}, Env: map[string]string{"A": "1"}}}
	}))
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmds, err := cat.Commands(context.Background(), target)
			if err != nil || len(cmds) != 1 || cmds[0].Key != "dev" {
				t.Errorf("%+v %v", cmds, err)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("the detector ran %d times for one target, want 1", n)
	}
}

// Different targets do not wait for each other.
func TestCatalogTargetsScanIndependently(t *testing.T) {
	repos := devserverstest.NewRepos()
	a, b := devservers.Target{Repo: "rep_a"}, devservers.Target{Repo: "rep_b"}
	repos.Track(a, t.TempDir())
	dirB := t.TempDir()
	repos.Track(b, dirB)
	block := make(chan struct{})
	cat := devservers.NewCatalog(repos, devserverstest.NewClock(), devservers.WithDetector(func(dir string) []devservers.DevCommand {
		if dir != dirB {
			<-block
		}
		return []devservers.DevCommand{{Key: "dev"}}
	}))
	go func() { _, _ = cat.Commands(context.Background(), a) }()
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() { _, _ = cat.Commands(context.Background(), b); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a scan of one target blocked another")
	}
	close(block)
}

// A caller cannot change what the next caller gets.
func TestCatalogCommandsReturnsACopy(t *testing.T) {
	r := newCatRig(t)
	ctx := context.Background()
	for range 2 { // a miss and then a hit
		cmds, err := r.cat.Commands(ctx, r.t)
		if err != nil {
			t.Fatal(err)
		}
		cmds[0].Argv[0] = "rm"
		cmds[0].Key = "hacked"
		cmds[0].Env = map[string]string{"X": "1"}
		cmds = cmds[:0]
		_ = cmds
	}
	got, _ := r.cat.Commands(ctx, r.t)
	if got[0].Key != "dev" || got[0].Argv[0] != "npm" || got[0].Env["X"] != "" {
		t.Fatalf("a caller changed the cached commands: %+v", got[0])
	}
	argv, env, err := r.cat.DevServer(ctx, r.t, "dev")
	if err != nil {
		t.Fatal(err)
	}
	argv[0] = "rm"
	env["Y"] = "1"
	argv2, env2, _ := r.cat.DevServer(ctx, r.t, "dev")
	if argv2[0] != "npm" || env2["Y"] != "" {
		t.Fatalf("DevServer shares its result: %v %v", argv2, env2)
	}
}

// An Invalidate while a scan runs is not undone by that scan's result.
func TestCatalogInvalidateDuringScanIsNotCached(t *testing.T) {
	repos, dir := devserverstest.NewRepos(), t.TempDir()
	target := devservers.Target{Repo: "rep_1"}
	repos.Track(target, dir)
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	cat := devservers.NewCatalog(repos, devserverstest.NewClock(), devservers.WithDetector(func(string) []devservers.DevCommand {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return []devservers.DevCommand{{Key: "dev"}}
	}))
	done := make(chan struct{})
	go func() { _, _ = cat.Commands(context.Background(), target); close(done) }()
	<-started
	cat.Invalidate("rep_1")
	close(release)
	<-done
	_, _ = cat.Commands(context.Background(), target)
	if calls.Load() != 2 {
		t.Fatalf("scans = %d, want 2: the stale scan was cached", calls.Load())
	}
}
