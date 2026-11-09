package devservers_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
)

type catRig struct {
	repos *devserverstest.Repos
	clock *devserverstest.FakeClock
	cat   *devservers.Catalog
	calls int
	dir   string
	t     devservers.Target
}

func newCatRig(t *testing.T) *catRig {
	t.Helper()
	r := &catRig{repos: devserverstest.NewRepos(), clock: devserverstest.NewClock(), dir: t.TempDir(), t: devservers.Target{Repo: "rep_1"}}
	r.repos.Track(r.t, r.dir)
	r.cat = devservers.NewCatalog(r.repos, r.clock, devservers.WithDetector(func(string) []devservers.DevCommand {
		r.calls++
		return []devservers.DevCommand{{Key: "web", Argv: []string{"npm", "run", "web"}}, {Key: "dev", Argv: []string{"npm", "run", "dev"}}}
	}))
	return r
}

// Listing the servers must not scan the tree on every request.
func TestCatalogScansOnceUntilInvalidatedOrExpired(t *testing.T) {
	r := newCatRig(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		cmds, err := r.cat.Commands(ctx, r.t)
		if err != nil || len(cmds) != 2 || cmds[0].Key != "dev" || cmds[1].Key != "web" {
			t.Fatalf("commands = %+v, %v", cmds, err)
		}
	}
	if r.calls != 1 {
		t.Fatalf("scans = %d, want 1", r.calls)
	}
	r.cat.Invalidate("rep_1")
	_, _ = r.cat.Commands(ctx, r.t)
	if r.calls != 2 {
		t.Fatalf("scans after Invalidate = %d, want 2", r.calls)
	}
	r.cat.InvalidateAll()
	_, _ = r.cat.Commands(ctx, r.t)
	if r.calls != 3 {
		t.Fatalf("scans after InvalidateAll = %d, want 3", r.calls)
	}
	r.clock.Advance(devservers.CatalogTTL)
	_, _ = r.cat.Commands(ctx, r.t)
	if r.calls != 4 {
		t.Fatalf("scans after the TTL = %d, want 4", r.calls)
	}
}

// A target that is not tracked offers nothing, even with a warm cache (a
// refused run folder takes the same path: nothing is read from it).
func TestCatalogAsksReposEveryTime(t *testing.T) {
	r := newCatRig(t)
	ctx := context.Background()
	if _, err := r.cat.Commands(ctx, r.t); err != nil {
		t.Fatal(err)
	}
	r.repos.Untrack(r.t)
	_, err := r.cat.Commands(ctx, r.t)
	if c, _ := apperr.ClassOf(err); c != apperr.NotFound {
		t.Fatalf("untracked = %v", err)
	}
	if _, _, err := r.cat.DevServer(ctx, r.t, "dev"); err == nil {
		t.Fatal("DevServer of an untracked target")
	}
}

func TestCatalogDevServerUsesTheSnapshotAndRejectsUnknownCommands(t *testing.T) {
	r := newCatRig(t)
	ctx := context.Background()
	argv, _, err := r.cat.DevServer(ctx, r.t, "web")
	if err != nil || len(argv) != 3 || argv[2] != "web" {
		t.Fatalf("argv = %v, %v", argv, err)
	}
	_, _, err = r.cat.DevServer(ctx, r.t, "nope")
	if c, _ := apperr.ClassOf(err); c != apperr.Invalid {
		t.Fatalf("unknown command = %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("scans = %d, want 1", r.calls)
	}
}

// The real detection derives each key once: the first is "dev", later ones come
// from their names and never collide.
func TestDetectDevCommandsKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"dev":"vite"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmds := devservers.DetectDevCommands(dir)
	if len(cmds) == 0 || cmds[0].Key != "dev" {
		t.Fatalf("commands = %+v", cmds)
	}
	seen := map[string]bool{}
	for _, c := range cmds {
		if seen[c.Key] {
			t.Fatalf("duplicate key %q", c.Key)
		}
		seen[c.Key] = true
	}
	if got := devservers.DetectDevCommands(filepath.Join(dir, "missing")); len(got) != 0 {
		t.Fatalf("missing folder: %+v", got)
	}
}
