package proposals

import (
	"context"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// StoreResolver is the Resolver backed by the database.
type StoreResolver struct{ Store *db.Store }

var _ Resolver = StoreResolver{}

// Repo returns the repo; Tracked is false for an untracked repo.
func (r StoreResolver) Repo(ctx context.Context, id core.RepoID) (RepoInfo, error) {
	repo, err := r.Store.GetRepo(ctx, id)
	if err == nil {
		return RepoInfo{ID: repo.ID, Path: repo.Path, Tracked: true, Name: repo.Name, DefaultBranch: repo.DefaultBranch}, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return RepoInfo{}, fmt.Errorf("read repo %s: %w", id, err)
	}
	repo, err = r.Store.GetRepoIncludingUntracked(ctx, id)
	if err != nil {
		return RepoInfo{}, fmt.Errorf("read repo %s: %w", id, err)
	}
	return RepoInfo{ID: repo.ID, Path: repo.Path, Tracked: false, Name: repo.Name, DefaultBranch: repo.DefaultBranch}, nil
}

// Worktree returns a task worktree.
func (r StoreResolver) Worktree(ctx context.Context, id core.WorktreeID) (WorktreeInfo, error) {
	w, err := r.Store.GetWorktree(ctx, id)
	if err != nil {
		return WorktreeInfo{}, fmt.Errorf("read worktree %s: %w", id, err)
	}
	return WorktreeInfo{ID: w.ID, Repo: w.RepoID, Task: w.TaskID, Path: w.Path, Branch: w.Branch, Name: w.Name, Status: w.Status}, nil
}

// Task returns the repos of a task.
func (r StoreResolver) Task(ctx context.Context, id core.TaskID) (TaskInfo, error) {
	t, err := r.Store.GetTask(ctx, id)
	if err != nil {
		return TaskInfo{}, fmt.Errorf("read task %s: %w", id, err)
	}
	return TaskInfo{ID: t.ID, Repos: t.RepoIDs}, nil
}
