package standup

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// Consumer-local sources of the standup. Each has a fake and a contract suite
// (bounds, time filtering, missing source, cancellation) run against the live
// implementation. A source that cannot be read returns an error the service
// turns into a labelled Unavailable entry; it never invents data.

// ScopeReader returns the tracked, READY repos of a project (at most
// corestandup.MaxRepos) with their names; an untracked project is
// db.ErrNotFound.
type ScopeReader interface {
	Repos(ctx context.Context, project core.ProjectID) ([]RepoRef, error)
}

// RepoRef is one repo in scope.
type RepoRef struct {
	ID   core.RepoID
	Name string
}

// CommitSource reads commits (repos.HistoryReader through the safe git wrapper).
type CommitSource = repos.HistoryReader

// MRSource reads merge requests from the CACHED host data (no network call on a
// refresh), within [from, to), at most max per repo.
type MRSource interface {
	MRs(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]corestandup.MR, bool, error)
}

// BuildSource reads builds of a repo from the store within [from, to).
type BuildSource interface {
	Builds(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]corestandup.Build, bool, error)
}

// Polisher submits the explicit polish task through the ordinary action path
// (Ask mode, RequestID-bound). It is called only for user input, never from a
// refresh.
type Polisher interface {
	SubmitPolish(ctx context.Context, id core.RequestID, project core.ProjectID, text, agent, model string) (core.TaskID, error)
}

// Deps is the aggregate the standup task builds the service from.
type Deps struct {
	Scope   ScopeReader
	Commits CommitSource
	MRs     MRSource
	Builds  BuildSource
	Polish  Polisher
	Hub     api.Phase6Hub
	// Clock is the shell's time; the core never reads one.
	Clock func() time.Time
}
