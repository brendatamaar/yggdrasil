package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// p5World is a database with one project (a folder with a space in its name)
// and two tracked repos, each with one active worktree, all in real folders.
// IDs are fixed so the shared contract suites can name them.
type p5World struct {
	store          *db.Store
	project        core.Project
	repoA, repoB   core.Repo
	wtA, wtB       core.Worktree
	dirs           trackedDirs
	redactor       safety.Redactor
	wtADir, wtBDir string
}

func newP5World(t *testing.T) *p5World {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	red := safety.NewRedactor()
	store := db.NewStore(d, red.Redact)
	w := &p5World{store: store, redactor: red}
	w.project = dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: filepath.Join(t.TempDir(), "acme shop")})
	mk := func(id core.RepoID, name string) core.Repo {
		r := dbtest.Repo(t, store, core.Repo{ID: id, ProjectID: w.project.ID, Name: name, Path: filepath.Join(w.project.Path, name)})
		if err := os.MkdirAll(r.Path, 0o755); err != nil {
			t.Fatal(err)
		}
		return r
	}
	w.repoA, w.repoB = mk("repo_a", "shop-api"), mk("repo_b", "shop-web")
	task := func(r core.Repo) core.Task {
		return dbtest.Task(t, store, core.Task{ProjectID: w.project.ID, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{r.ID}, Mode: core.ModeCode, Status: core.TaskReview})
	}
	wt := func(id core.WorktreeID, r core.Repo, name string) (core.Worktree, string) {
		dir := filepath.Join(filepath.Dir(w.project.Path), "work trees", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		tid := task(r).ID
		var out core.Worktree
		err := store.Commit(context.Background(), func(tx *db.Tx) error {
			var err error
			out, err = tx.CreateWorktree(context.Background(), core.Worktree{
				ID: id, RepoID: r.ID, TaskID: tid, Name: name, Path: dir, Branch: "ygg/" + name, CreatedAt: time.Now().UTC(),
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out, dir
	}
	w.wtA, w.wtADir = wt("wt_a", w.repoA, "cart")
	w.wtB, w.wtBDir = wt("wt_b", w.repoB, "checkout")
	w.dirs = trackedDirs{store}
	return w
}

// The live Repos adapter (the tracked-folder resolver over the database) must
// pass the same suite as the fake (invariant 6, DV-05).
func TestPhase5ContractDevServerRepos(t *testing.T) {
	devserverstest.ReposContract(t, func(t *testing.T) devserverstest.ReposFixture {
		w := newP5World(t)
		return devserverstest.ReposFixture{
			Repos:     devRepos{w.dirs},
			Main:      devservers.Target{Repo: w.repoA.ID},
			Worktree:  devservers.Target{Repo: w.repoA.ID, Worktree: w.wtA.ID},
			Unknown:   devservers.Target{Repo: "rep_zz"},
			UnknownWT: devservers.Target{Repo: w.repoA.ID, Worktree: "wt_zz"},
			WrongRepo: devservers.Target{Repo: w.repoB.ID, Worktree: w.wtA.ID},
			Untrack:   func() { dbtest.UntrackRepo(t, w.store, w.repoA) },
		}
	})
}

// A symlink or junction leaf is refused for a run folder (it could point out of
// the tracked folders), and a folder deleted behind our back is untracked.
func TestPhase5ContractDevServerReposRunFolderChecks(t *testing.T) {
	w := newP5World(t)
	ctx := context.Background()
	r := devRepos{w.dirs}
	if err := os.RemoveAll(w.wtADir); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Dir(ctx, devservers.Target{Repo: w.repoA.ID, Worktree: w.wtA.ID}); err == nil {
		t.Fatal("a worktree folder that no longer exists resolved")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, w.wtADir); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if dir, err := r.Dir(ctx, devservers.Target{Repo: w.repoA.ID, Worktree: w.wtA.ID}); err == nil {
		t.Fatalf("a symlinked worktree resolved to %q", dir)
	}
}
