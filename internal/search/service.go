package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
)

// Defaults of a search (design: Caps and S2).
const (
	DefaultParallel       = 8
	DefaultDeadline       = 15 * time.Second
	DefaultPerRepoTimeout = 12 * time.Second
)

// RepoSource lists the active tracked repos. It is the only way a search finds
// a folder: a path never comes from the client (invariant 6, S3).
type RepoSource interface {
	ActiveRepos(ctx context.Context) ([]coresearch.RepoRef, error)
}

// Deps are the capabilities of a Service.
type Deps struct {
	Repos RepoSource
	Grep  Grepper
	// Editor returns the saved editor setting ("" when none).
	Editor func(ctx context.Context) string
	Log    *slog.Logger
	// Zero values take the defaults above.
	Parallel       int
	Deadline       time.Duration
	PerRepoTimeout time.Duration
}

// Service runs searches. It is safe for concurrent use.
type Service struct{ d Deps }

// NewService checks the dependencies.
func NewService(d Deps) (*Service, error) {
	if d.Repos == nil || d.Grep == nil {
		return nil, errors.New("search: a repo source and a grepper are required")
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Parallel <= 0 {
		d.Parallel = DefaultParallel
	}
	if d.Deadline <= 0 {
		d.Deadline = DefaultDeadline
	}
	if d.PerRepoTimeout <= 0 {
		d.PerRepoTimeout = DefaultPerRepoTimeout
	}
	return &Service{d: d}, nil
}

// Editor returns the editor for links, or "" when none is set or the saved
// value is unknown.
func (s *Service) Editor(ctx context.Context) coresearch.Editor {
	if s.d.Editor == nil {
		return ""
	}
	e, err := coresearch.ParseEditor(s.d.Editor(ctx))
	if err != nil {
		return ""
	}
	return e
}

// Search runs q over the active tracked repos it names (all of them when
// q.Repos is empty) and returns the final core state. A repo id that is
// unknown or untracked is refused for the whole request. It returns an error
// only when the request fails as a whole; per-repo failures are in
// Search.Problems. When ctx ends first (the client went away) it returns
// ctx's error and no git process is left.
func (s *Service) Search(ctx context.Context, q coresearch.Query) (coresearch.Search, error) {
	if err := q.Validate(); err != nil {
		return coresearch.Search{}, apperr.New(apperr.Invalid, "The search is not valid.", err)
	}
	if err := CheckRegex(q); err != nil {
		return coresearch.Search{}, err
	}
	active, err := s.d.Repos.ActiveRepos(ctx)
	if err != nil {
		return coresearch.Search{}, fmt.Errorf("list tracked repos: %w", err)
	}
	repos, err := scope(active, q.Repos)
	if err != nil {
		return coresearch.Search{}, err
	}

	runCtx, cancelAll := context.WithTimeout(ctx, s.d.Deadline)
	defer cancelAll()

	state, cmds := coresearch.Step(coresearch.Search{}, coresearch.Begin{Query: q, Repos: repos})
	inputs := make(chan coresearch.Input, len(repos)) // one answer per repo: workers never block
	var g errgroup.Group
	g.SetLimit(s.d.Parallel)
	for _, c := range cmds {
		if cmd, ok := c.(coresearch.Grep); ok {
			g.Go(func() error {
				inputs <- s.worker(runCtx, cmd)
				return nil
			})
		}
	}

	for state.Searching() {
		select {
		case in := <-inputs:
			var out []coresearch.Command
			state, out = coresearch.Step(state, in)
			s.run(out, cancelAll)
		case <-runCtx.Done():
			if ctx.Err() != nil { // the client went away
				_ = g.Wait()
				return coresearch.Search{}, ctx.Err()
			}
			var out []coresearch.Command
			state, out = coresearch.Step(state, coresearch.Deadline{})
			s.run(out, cancelAll)
		}
	}
	cancelAll()
	_ = g.Wait() // S1: no git process outlives the request
	return state, nil
}

// run interprets the commands of a step.
func (s *Service) run(cmds []coresearch.Command, cancelAll context.CancelFunc) {
	for _, c := range cmds {
		if _, ok := c.(coresearch.CancelAll); ok {
			cancelAll()
		}
	}
}

// worker interprets one Grep command and returns the input for the core.
func (s *Service) worker(ctx context.Context, cmd coresearch.Grep) coresearch.Input {
	if ctx.Err() != nil { // a slot freed after the deadline: start nothing
		return coresearch.RepoFailed{Repo: cmd.Repo.ID, Kind: coresearch.ProblemTimedOut}
	}
	rctx, cancel := context.WithTimeout(ctx, s.d.PerRepoTimeout)
	defer cancel()
	res, err := s.d.Grep.Grep(rctx, cmd.Repo.Path, cmd.Query)
	if err != nil {
		kind := coresearch.ProblemGitFailed
		switch {
		case errors.Is(err, ErrMissing):
			kind = coresearch.ProblemMissing
		case errors.Is(err, context.DeadlineExceeded):
			kind = coresearch.ProblemTimedOut
		}
		s.d.Log.Debug("search: repo not searched", "repo", cmd.Repo.ID, "kind", kind, "err", err)
		return coresearch.RepoFailed{Repo: cmd.Repo.ID, Kind: kind}
	}
	if res.Skipped > 0 {
		s.d.Log.Debug("search: skipped unparseable lines", "repo", cmd.Repo.ID, "lines", res.Skipped)
	}
	return coresearch.RepoResult{Repo: cmd.Repo.ID, Matches: res.Matches, Truncated: res.Truncated}
}

// scope picks the repos of the request from the active ones. An id that is
// not active is refused, never searched.
func scope(active []coresearch.RepoRef, want []core.RepoID) ([]coresearch.RepoRef, error) {
	if len(want) == 0 {
		return active, nil
	}
	var out []coresearch.RepoRef
	for _, id := range want {
		i := slices.IndexFunc(active, func(r coresearch.RepoRef) bool { return r.ID == id })
		if i < 0 {
			return nil, apperr.New(apperr.Invalid, "One of the selected repos is not tracked. Refresh the page and pick from the tracked repos.", nil)
		}
		out = append(out, active[i])
	}
	return out, nil
}
