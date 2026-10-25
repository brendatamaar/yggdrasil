package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/search"

	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// searchRepos lists the tracked repos of every project (invariant 6): an
// untracked repo is invisible to the store's list reads.
type searchRepos struct{ store *db.Store }

func (l searchRepos) ActiveRepos(ctx context.Context) ([]coresearch.RepoRef, error) {
	projects, err := l.store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	var out []coresearch.RepoRef
	for _, p := range projects {
		rs, err := l.store.ListRepos(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("list repos of %s: %w", p.ID, err)
		}
		for _, r := range rs {
			out = append(out, coresearch.RepoRef{ID: r.ID, Name: r.Name, Path: r.Path})
		}
	}
	return out, nil
}

// newSearchService wires code search (XR-01) to the store and to git.
func newSearchService(store *db.Store, log *slog.Logger) (*search.Service, error) {
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		return nil, fmt.Errorf("prepare the search environment: %w", err)
	}
	grep, err := search.NewGitGrepper(env, platform.Start)
	if err != nil {
		return nil, fmt.Errorf("prepare git grep: %w", err)
	}
	return search.NewService(search.Deps{
		Repos: searchRepos{store}, Grep: grep, Log: log,
		Editor: func(ctx context.Context) string {
			s, err := store.GetSetting(ctx, repos.EditorSettingKey)
			if err != nil {
				return ""
			}
			return s.Value
		},
	})
}
