package standup

import (
	"context"
	"errors"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
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
//
// key is corestandup.PolishKey: the stable identity of the request (project,
// range, agent, model, request ID), never the rebuilt text. FindPolish answers a
// repeat from the stored link before any text is rebuilt: the task already made
// for this request ID (found), or none yet, or ErrRequestReused when the ID was
// used for another request. SubmitPolish stores the key with the task, so a
// repeat after a lost reply or a restart returns the first task even though the
// text, rebuilt from newer data, differs.
type Polisher interface {
	FindPolish(ctx context.Context, id core.RequestID, key string) (core.TaskID, bool, error)
	SubmitPolish(ctx context.Context, id core.RequestID, key string, project core.ProjectID, text, agent, model string) (core.TaskID, error)
}

// ErrRequestReused is returned by a Polisher when the request ID was already
// used for a different request (another range, agent or model).
var ErrRequestReused = errors.New("standup polish request id reused for another request")

// Deps is the aggregate the standup task builds the service from.
type Deps struct {
	Scope   ScopeReader
	Commits CommitSource
	MRs     MRSource
	Builds  BuildSource
	Polish  Polisher
	Hub     api.Phase6Hub
	// Redactor masks tokens in commit subjects and authors before they enter a
	// summary (invariant 4). Nil means safety.NewRedactor().
	Redactor safety.Redactor
	// Clock is the shell's time; the core never reads one.
	Clock func() time.Time
}
