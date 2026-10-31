package dbtest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Task inserts a task (draft unless task.Status is set) and fails the test on
// error. A missing ID, title, agent and time is filled in.
func Task(t testing.TB, s *db.Store, task core.Task) core.Task {
	t.Helper()
	now := time.Now().UTC()
	if task.ID == "" {
		task.ID = core.TaskID(db.NewID("tsk"))
	}
	if task.Title == "" {
		task.Title = "t"
	}
	if task.Agent == "" {
		task.Agent = "mock"
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt, task.UpdatedAt = now, now
	}
	var out core.Task
	commit(t, s, func(ctx context.Context, tx *db.Tx) error {
		var err error
		out, err = tx.CreateTask(ctx, task)
		return err
	})
	return out
}

// TaskWorktree inserts an active worktree of a task in repo and fails the test
// on error. name must be different for each call.
func TaskWorktree(t testing.TB, s *db.Store, task core.TaskID, repo core.Repo, name, branch string) core.Worktree {
	t.Helper()
	var w core.Worktree
	commit(t, s, func(ctx context.Context, tx *db.Tx) error {
		var err error
		w, err = tx.CreateWorktree(ctx, core.Worktree{
			ID: core.WorktreeID(db.NewID("wt")), RepoID: repo.ID, TaskID: task, Name: name,
			Path: filepath.Join(repo.Path, name), Branch: branch, CreatedAt: time.Now().UTC(),
		})
		return err
	})
	return w
}

// UntrackRepo hides a repo, as untracking it in the UI does, and fails the
// test on error.
func UntrackRepo(t testing.TB, s *db.Store, repo core.Repo) {
	t.Helper()
	ws, err := s.GetWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := ws.Projects[repo.ProjectID]
	if !ok {
		t.Fatalf("dbtest.UntrackRepo: unknown project %s", repo.ProjectID)
	}
	commit(t, s, func(ctx context.Context, tx *db.Tx) error {
		return tx.Workspace().MarkRepoUntracked(ctx, corerepos.MarkRepoUntracked{
			ProjectID: repo.ProjectID, RepoID: repo.ID, At: time.Now().UTC(), ExpectVersion: proj.Version,
		})
	})
}
