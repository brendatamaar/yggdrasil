package verify_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/buildstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/verify"
	"github.com/brendatamaar/yggdrasil/internal/verify/verifytest"
)

func TestFakeRunnerContract(t *testing.T) {
	verifytest.BuildRunnerContract(t, func(t *testing.T) verifytest.RunnerHarness {
		r := verifytest.NewRunner()
		return verifytest.RunnerHarness{
			Runner: r, Repo: "rep_a", Worktree: "wt_a",
			WaitFinished: func(t *testing.T, id core.BuildID) corebuilds.State {
				st, err := r.Get(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				return st
			},
		}
	})
}

type wtLocator struct {
	repo string
	wt   string
}

func (l wtLocator) RepoDir(context.Context, core.RepoID) (string, error) { return l.repo, nil }
func (l wtLocator) WorktreeDir(context.Context, core.WorktreeID) (string, error) {
	return l.wt, nil
}
func (l wtLocator) Known(context.Context) (builds.Known, error) {
	return builds.Known{
		Repos:     map[core.RepoID]struct{}{"rep_a": {}},
		Worktrees: map[core.WorktreeID]core.RepoID{"wt_a": "rep_a"},
	}, nil
}

// TestRealRunnerContract runs the suite against the real builds service with
// the fake process starter.
func TestRealRunnerContract(t *testing.T) {
	verifytest.BuildRunnerContract(t, func(t *testing.T) verifytest.RunnerHarness {
		dir := filepath.Join(t.TempDir(), "my wt")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		fake := &platformtest.Fake{}
		svc, err := builds.NewService(builds.Deps{
			Store: buildstest.NewFake(map[core.RepoID]core.ProjectID{"rep_a": "prj_1"}), Start: fake.Start,
			Locator: wtLocator{repo: dir, wt: dir}, Env: safety.NewEnvBuilder(),
			Resolve: func(name, _ string, _ []string) (string, error) { return name, nil },
			DataDir: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- svc.Start(ctx) }()
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("builds service did not stop")
			}
		})
		return verifytest.RunnerHarness{
			Runner: svc, Repo: "rep_a", Worktree: "wt_a",
			WaitFinished: func(t *testing.T, id core.BuildID) corebuilds.State {
				deadline := time.Now().Add(10 * time.Second)
				for {
					st, err := svc.Get(context.Background(), id)
					// Artifacts are collected after a success only; the result is
					// final before that, but wait so the state stops changing.
					if f, ok := st.(corebuilds.Finished); err == nil && ok {
						if _, passed := f.Result.(corebuilds.ResultSucceeded); !passed || f.Artifacts != nil {
							return st
						}
					}
					if time.Now().After(deadline) {
						t.Fatalf("timeout waiting for %s: %#v %v", id, st, err)
					}
					time.Sleep(5 * time.Millisecond)
				}
			},
		}
	})
}

func TestFakeStoreContract(t *testing.T) {
	verifytest.VerifyStoreContract(t, func(t *testing.T) verifytest.StoreHarness {
		s := verifytest.NewStore()
		for i, repo := range []core.RepoID{"rep_1", "rep_2"} {
			s.AddWorktree(core.Worktree{ID: core.WorktreeID([]string{"wt_1", "wt_2"}[i]), RepoID: repo, TaskID: "tsk_1"}, "base")
		}
		s.AddWorktree(core.Worktree{ID: "wt_3", RepoID: "rep_3", TaskID: "tsk_1", Status: core.WorktreeMerged}, "base")
		return verifytest.StoreHarness{
			Store: s, Task: "tsk_1", Repos: [2]core.RepoID{"rep_1", "rep_2"}, Worktrees: [2]core.WorktreeID{"wt_1", "wt_2"},
			MergedRepo: "rep_3", MergedWorktree: "wt_3",
			Audit: func(subject string) []core.AuditEntry {
				var out []core.AuditEntry
				for _, e := range s.Audit() {
					if e.Subject == subject {
						out = append(out, e)
					}
				}
				return out
			},
		}
	})
}

func newSQLite(t *testing.T) *db.Store {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return db.NewStore(d, safety.NewRedactor().Redact)
}

func TestSQLiteStoreContract(t *testing.T) {
	verifytest.VerifyStoreContract(t, func(t *testing.T) verifytest.StoreHarness {
		ctx := context.Background()
		store := newSQLite(t)
		project := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
		var repos [3]core.Repo
		for i, name := range []string{"shop-api", "shop-web", "shop-admin"} {
			dir := filepath.Join(project.Path, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			repos[i] = dbtest.Repo(t, store, core.Repo{ProjectID: project.ID, Name: name, Path: dir})
		}
		task := dbtest.Task(t, store, core.Task{
			ProjectID: project.ID, Scope: core.ScopeProject, Mode: core.ModeCode, RepoIDs: []core.RepoID{repos[0].ID, repos[1].ID, repos[2].ID},
		})
		var wts [3]core.Worktree
		for i := range wts {
			wts[i] = dbtest.TaskWorktree(t, store, task.ID, repos[i], "wt"+string(rune('a'+i)), "ygg/b"+string(rune('a'+i)))
		}
		if err := store.Commit(ctx, func(tx *db.Tx) error {
			return tx.UpdateWorktreeStatus(ctx, wts[2].ID, core.WorktreeMerged)
		}); err != nil {
			t.Fatal(err)
		}
		return verifytest.StoreHarness{
			Store: verify.NewSQLiteStore(store), Task: task.ID,
			Repos: [2]core.RepoID{repos[0].ID, repos[1].ID}, Worktrees: [2]core.WorktreeID{wts[0].ID, wts[1].ID},
			MergedRepo: repos[2].ID, MergedWorktree: wts[2].ID,
			Audit: func(subject string) []core.AuditEntry {
				out, err := store.ListAudit(ctx, subject, 100)
				if err != nil {
					t.Fatal(err)
				}
				return out
			},
		}
	})
}
