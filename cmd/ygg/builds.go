package main

import (
	"context"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// buildLocator answers the builds service's questions about folders from the
// store's active reads: only tracked repos can be built (invariant 6).
type buildLocator struct{ store *db.Store }

func (l buildLocator) RepoDir(ctx context.Context, id core.RepoID) (string, error) {
	r, err := l.store.GetRepo(ctx, id)
	if err != nil {
		return "", fmt.Errorf("find repo %s: %w", id, err)
	}
	return r.Path, nil
}

func (l buildLocator) WorktreeDir(ctx context.Context, id core.WorktreeID) (string, error) {
	w, err := l.store.GetWorktree(ctx, id)
	if err != nil {
		return "", fmt.Errorf("find worktree %s: %w", id, err)
	}
	return w.Path, nil
}

// Known lists the tracked repos. Worktrees arrive with Code mode (Phase 3);
// until then no build can target one.
func (l buildLocator) Known(ctx context.Context) (builds.Known, error) {
	k := builds.Known{Repos: map[core.RepoID]struct{}{}, Worktrees: map[core.WorktreeID]core.RepoID{}}
	projects, err := l.store.ListProjects(ctx)
	if err != nil {
		return builds.Known{}, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		repos, err := l.store.ListRepos(ctx, p.ID)
		if err != nil {
			return builds.Known{}, fmt.Errorf("list repos of %s: %w", p.ID, err)
		}
		for _, r := range repos {
			k.Repos[r.ID] = struct{}{}
		}
	}
	return k, nil
}

// lateBuildsPublisher forwards build changes and log lines to the server,
// which is built after the builds service. srv is set once, before run
// starts any goroutine that could publish.
type lateBuildsPublisher struct{ srv *api.Server }

func (p *lateBuildsPublisher) PublishBuildChanged(id core.BuildID, status core.BuildStatus) {
	if p.srv != nil {
		p.srv.PublishBuildChanged(id, status)
	}
}

func (p *lateBuildsPublisher) PublishBuildLog(id core.BuildID, step, attempt int, line string) {
	if p.srv != nil {
		p.srv.PublishBuildLog(id, step, attempt, line)
	}
}

// lateBatchPublisher forwards git-action batch changes to the server, which is
// built after the actions service. srv is set once, before run starts.
type lateBatchPublisher struct{ srv *api.Server }

func (p *lateBatchPublisher) PublishGitBatch(batch corerepos.BatchID, repoIDs []core.RepoID, finished bool) {
	if p.srv != nil {
		p.srv.PublishGitBatch(batch, repoIDs, finished)
	}
}
