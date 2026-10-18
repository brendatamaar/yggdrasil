package db

import (
	"context"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// Test-only row writers. Production writes projects and repos only through
// Tx.Workspace (the tracking decisions, with their audit entries).

func (tx *Tx) CreateProject(ctx context.Context, p core.Project) (core.Project, error) {
	if err := needTime("project", p.CreatedAt); err != nil {
		return p, err
	}
	p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := p.Validate(); err != nil {
		return p, err
	}
	err := tx.q.CreateProject(ctx, gen.CreateProjectParams{
		ID: string(p.ID), Name: p.Name, Path: p.Path, CreatedAt: fmtTime(p.CreatedAt),
	})
	if err != nil {
		return p, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

func (tx *Tx) CreateRepo(ctx context.Context, r core.Repo) (core.Repo, error) {
	if err := needTime("repo", r.CreatedAt); err != nil {
		return r, err
	}
	r.CreatedAt = r.CreatedAt.UTC().Truncate(time.Millisecond)
	if err := r.Validate(); err != nil {
		return r, err
	}
	proj, err := tx.q.GetProjectAny(ctx, string(r.ProjectID))
	if err != nil {
		return r, found("create repo: project "+string(r.ProjectID), err)
	}
	all, err := tx.q.ListAllRepos(ctx)
	if err != nil {
		return r, err
	}
	taken := map[string]bool{}
	for _, o := range all {
		if o.ProjectID == proj.ID {
			taken[o.RelPath] = true
		}
	}
	rel, ok := derivedRel(proj.Path, r.Path)
	if !ok {
		rel = fallbackRel(r.Name, string(r.ID), taken)
	}
	err = tx.q.CreateRepo(ctx, gen.CreateRepoParams{
		ID: string(r.ID), ProjectID: string(r.ProjectID), Name: r.Name, Path: r.Path, RelPath: rel,
		DefaultBranch: r.DefaultBranch, Trusted: b2i(r.Trusted), CreatedAt: fmtTime(r.CreatedAt),
	})
	if err != nil {
		return r, fmt.Errorf("create repo: %w", err)
	}
	return r, nil
}
