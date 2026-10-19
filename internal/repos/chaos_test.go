package repos_test

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// failNthAudit makes the (n+1)th audit insert from now on abort, from a second
// connection to the same file: whatever the service wrote before that point
// in its transaction is the "crash" window of tracking invariant S1.
func failNthAudit(t *testing.T, e *env, n int) {
	t.Helper()
	dsn, err := db.DSN(e.dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var base int
	if err := raw.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&base); err != nil {
		t.Fatal(err)
	}
	trigger := fmt.Sprintf(`CREATE TRIGGER chaos_fail BEFORE INSERT ON audit_log
		WHEN (SELECT count(*) FROM audit_log) = %d
		BEGIN SELECT RAISE(ABORT, 'chaos: crash after %d audit rows'); END`, base+n, n)
	if _, err := raw.Exec(trigger); err != nil {
		t.Fatal(err)
	}
}

type stored struct {
	ws     corerepos.Workspace
	audits int
	reload int
	pubs   int32
}

func snapshotOf(t *testing.T, e *env) stored {
	t.Helper()
	ws, err := e.store.GetWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	list, err := e.store.ListAudit(context.Background(), "", 10000)
	if err != nil {
		t.Fatal(err)
	}
	return stored{ws: ws, audits: len(list), reload: e.rec.reloadCount(), pubs: e.pub.n.Load()}
}

// TestTrackingS1_CrashLeavesAllOrNothing is shell invariant S1 of
// docs/design/repo-tracking.md: for every point at which one decision's audit
// rows could fail, the rows of the decision, its audit and its after-commit
// effects are all absent; the run without a failure changes the state.
func TestTrackingS1_CrashLeavesAllOrNothing(t *testing.T) {
	ctx := context.Background()
	scenarios := []struct {
		name  string
		setup func(e *env) func() error
		// minAudits: how many audit rows the decision writes (the failure
		// points the loop must reach).
		minAudits int
	}{
		{"track a new folder", func(e *env) func() error {
			e.repoDir("shop-api")
			e.repoDir("shop-web")
			return func() error {
				_, err := e.svc.Track(ctx, e.root, "acme-shop", []corerepos.Pick{pick("shop-api"), pick("shop-web")}, "user")
				return err
			}
		}, 3},
		{"add repos to a project", func(e *env) func() error {
			e.repoDir("shop-api")
			e.repoDir("shop-web")
			e.repoDir("shop-admin")
			id := e.track("acme-shop", "shop-api").(corerepos.Tracked).ProjectID
			return func() error {
				_, err := e.svc.TrackRepos(ctx, id, []corerepos.Pick{pick("shop-web"), pick("shop-admin")}, "user")
				return err
			}
		}, 2},
		{"untrack a project with repos", func(e *env) func() error {
			e.repoDir("shop-api")
			e.repoDir("shop-web")
			id := e.track("acme-shop", "shop-api", "shop-web").(corerepos.Tracked).ProjectID
			return func() error { _, err := e.svc.UntrackProject(ctx, id, "user"); return err }
		}, 3},
		{"untrack one repo", func(e *env) func() error {
			e.repoDir("shop-api")
			out := e.track("acme-shop", "shop-api").(corerepos.Tracked)
			return func() error { _, err := e.svc.UntrackRepo(ctx, out.Added[0], "user"); return err }
		}, 1},
		{"restore an untracked project", func(e *env) func() error {
			e.repoDir("shop-api")
			id := e.track("acme-shop", "shop-api").(corerepos.Tracked).ProjectID
			if _, err := e.svc.UntrackProject(ctx, id, "user"); err != nil {
				e.t.Fatal(err)
			}
			return func() error {
				_, err := e.svc.Track(ctx, e.root, "acme-shop-2", []corerepos.Pick{pick("shop-api")}, "user")
				return err
			}
		}, 2},
		{"rename a project", func(e *env) func() error {
			e.repoDir("shop-api")
			id := e.track("acme-shop", "shop-api").(corerepos.Tracked).ProjectID
			return func() error { _, err := e.svc.RenameProject(ctx, id, "acme-shop-v2", "user"); return err }
		}, 1},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			failures := 0
			for n := 0; n < 10; n++ {
				e := newEnv(t)
				op := sc.setup(e)
				before := snapshotOf(t, e)
				failNthAudit(t, e, n)
				err := op()
				after := snapshotOf(t, e)
				if err == nil {
					if reflect.DeepEqual(before.ws, after.ws) {
						t.Fatalf("n=%d: the decision succeeded but changed no rows", n)
					}
					if after.audits <= before.audits {
						t.Fatalf("n=%d: rows changed without an audit entry", n)
					}
					break
				}
				failures++
				if !reflect.DeepEqual(before.ws, after.ws) {
					t.Errorf("n=%d: a failed decision left rows behind:\nbefore %+v\nafter  %+v", n, before.ws, after.ws)
				}
				if after.audits != before.audits {
					t.Errorf("n=%d: a failed decision left %d audit rows", n, after.audits-before.audits)
				}
				if after.reload != before.reload || after.pubs != before.pubs {
					t.Errorf("n=%d: effects ran after a failed commit (reloads %d->%d, publishes %d->%d)",
						n, before.reload, after.reload, before.pubs, after.pubs)
				}
			}
			if failures < sc.minAudits {
				t.Errorf("only %d failure points reached, want at least %d (the crash window is not covered)", failures, sc.minAudits)
			}
		})
	}
}

// TestTrackingS2_FullLifecycleLeavesTheTreeAlone hashes a temp tree (with a
// CRLF file, a folder with a space and a nested repo) around the whole
// lifecycle: preview, track, rescan, add, rename, untrack, restore, browse.
func TestTrackingS2_FullLifecycleLeavesTheTreeAlone(t *testing.T) {
	e := newEnv(t)
	e.repoDir("shop api")
	e.repoDir("libs/shop-web")
	e.repoDir("shop-admin")
	before := treeHash(t, e.root)
	ctx := context.Background()

	if _, err := e.svc.Preview(ctx, e.root); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.BrowseDirs(ctx, e.root); err != nil {
		t.Fatal(err)
	}
	out := e.track("acme-shop", "shop api").(corerepos.Tracked)
	if _, err := e.svc.Rescan(ctx, out.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.TrackRepos(ctx, out.ProjectID, []corerepos.Pick{pick("libs/shop-web"), pick("shop-admin")}, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RenameProject(ctx, out.ProjectID, "acme-shop-2", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UntrackRepo(ctx, out.Added[0], "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UntrackProject(ctx, out.ProjectID, "user"); err != nil {
		t.Fatal(err)
	}
	e.track("acme-shop-3", "shop api")
	if _, err := e.svc.Rescan(ctx, out.ProjectID); err != nil {
		t.Fatal(err)
	}
	if after := treeHash(t, e.root); after != before {
		t.Error("a tracking operation created, changed or removed a file or folder")
	}
}
