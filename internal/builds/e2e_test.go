package builds_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

type dbLocator struct {
	store *db.Store
	repo  core.Repo
}

func (l dbLocator) RepoDir(ctx context.Context, id core.RepoID) (string, error) {
	r, err := l.store.GetRepo(ctx, id)
	return r.Path, err
}

func (l dbLocator) WorktreeDir(context.Context, core.WorktreeID) (string, error) {
	return "", os.ErrNotExist
}

func (l dbLocator) Known(context.Context) (builds.Known, error) {
	return builds.Known{Repos: map[core.RepoID]struct{}{l.repo.ID: {}}, Worktrees: map[core.WorktreeID]core.RepoID{}}, nil
}

// TestEndToEndSQLiteRealProcess runs a real child process (a .cmd file on
// Windows) in a folder with spaces, with the SQLite store.
func TestEndToEndSQLiteRealProcess(t *testing.T) {
	ctx := context.Background()
	red := safety.NewRedactor(sentinel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := db.NewStore(d, red.Redact)
	project := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
	dir := filepath.Join(project.Path, "shop api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := dbtest.Repo(t, store, core.Repo{ProjectID: project.ID, Name: "shop-api", Path: dir})
	argv0 := writeHelper(t, dir)

	svc, err := builds.NewService(builds.Deps{
		Store: builds.NewSQLiteStore(store), Start: platform.Start, Locator: dbLocator{store: store, repo: repo},
		Env: safety.NewEnvBuilder(), Redactor: red, DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Start(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("service did not stop")
		}
	})

	id, err := svc.Enqueue(ctx, corebuilds.Spec{
		Target: corebuilds.MainCheckout{},
		Steps:  []corebuilds.StepSpec{{RepoID: repo.ID, Name: "hello", Argv: []string{argv0}}},
	}, "user")
	if err != nil {
		t.Fatal(err)
	}
	var f corebuilds.Finished
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := svc.Get(ctx, id)
		if fin, ok := st.(corebuilds.Finished); err == nil && ok && fin.Artifacts != nil {
			f = fin
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout; state %#v err %v", st, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := f.Result.(corebuilds.ResultSucceeded); !ok {
		t.Fatalf("result %#v", f.Result)
	}
	log, _, err := svc.StepLog(ctx, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "built in") || !strings.Contains(string(log), "shop api") {
		t.Errorf("log %q", log)
	}
	if strings.Contains(string(log), sentinel) {
		t.Errorf("log leaks the token: %q", log)
	}
}
