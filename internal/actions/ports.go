package actions

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// Consumer-local ports of the action service, each with a fake and a contract
// suite run against the live implementation (docs/AGENTS.md 5.11). Every method
// takes a context and returns plain core or shell values; none returns a token.

// ScopeReader resolves a RepoID to current tracked, READY scope and reads the
// repo facts the catalogue needs. A repo that is untracked, reserved or
// unknown is db.ErrNotFound. It never reads the filesystem beyond the existing
// bounded status.
type ScopeReader interface {
	// Facts returns the catalogue facts of a repo now, including its current HEAD.
	Facts(ctx context.Context, repo core.RepoID) (coreactions.Facts, error)
	// StillTracked rechecks, at execution, that the repo is tracked and ready.
	StillTracked(ctx context.Context, repo core.RepoID) error
	// NoiseFacts returns the bounded noise facts for ignore suggestions.
	NoiseFacts(ctx context.Context, repo core.RepoID) (corerepos.NoiseFacts, error)
	// Target returns the project of a tracked READY repo and its host identity,
	// resolved on the server (never from a browser URL).
	Target(ctx context.Context, repo core.RepoID) (Target, error)
	// ReposOf lists the tracked READY repos of a project (db.ErrNotFound when
	// the project is unknown). Untracked and reserved repos never appear.
	ReposOf(ctx context.Context, project core.ProjectID) ([]core.RepoID, error)
	// PullTarget returns the checked-out branch, its upstream and HEAD for a
	// fast-forward pull proposal.
	PullTarget(ctx context.Context, repo core.RepoID) (PullTarget, error)
}

// Target is the project and host identity of a tracked repo. HasHost is false
// for a repo with no linked host instance (an issue import is then refused).
type Target struct {
	Project     core.ProjectID
	HasHost     bool
	Instance    string
	HostProject string
}

// PullTarget is what a PullFF proposal pins.
type PullTarget struct {
	Branch   string
	Upstream string
	Head     string
}

// ContextReader loads bounded, redacted, source-labelled context for an
// offering: host reads (hosts.ContextReader) and the local build log. A missing
// token or an unreadable folder is a labelled gap in the result, not an error.
type ContextReader interface {
	// Host reads one host context key through the host service.
	Host(ctx context.Context, key corehosts.ContextKey) (hosts.ContextOutcome, error)
	// BuildLog returns at most coreactions.MaxContextBytes of the failed build's
	// redacted log.
	BuildLog(ctx context.Context, repo core.RepoID, build core.BuildID) (text string, truncated bool, err error)
	// Diff returns the exact current diff of the repo for commit grouping, with
	// the HEAD and diff hash proposals will pin.
	Diff(ctx context.Context, repo core.RepoID) (DiffContext, error)
}

// DiffContext is the bounded current diff of a repo.
type DiffContext struct {
	Head      string
	DiffHash  string
	Text      string
	Truncated bool
	// Paths are the changed paths, for explicit-path commit proposals.
	Paths []string
}

// TaskSubmitter submits tasks and drafts through internal/tasks.
type TaskSubmitter interface {
	tasks.RequestSubmitter
	tasks.DraftService
}

// ProposalCreator creates pending proposals for an action's result. Creation
// requests no confirmation and executes nothing; it is idempotent for a
// RequestID, so a retried dispatch finds the first proposals.
type ProposalCreator interface {
	CreateForRequest(ctx context.Context, id core.RequestID, items []ProposalSpec) ([]core.ProposalID, error)
	// FindForRequest is the read-only reconciliation: the proposals created for
	// id, or db.ErrNotFound when none exist. It never creates one.
	FindForRequest(ctx context.Context, id core.RequestID) ([]core.ProposalID, error)
}

// FolderDrafts stores a draft task for a repo whose folder cannot be read, so a
// diagnosis brief is kept and waits for a valid working directory. It starts
// no turn, process or worktree, and is idempotent per RequestID with a
// LookupRequest-style reconciliation through tasks.RequestSubmitter. Nil in
// Deps: such a request is refused with a plain explanation (no run is faked).
type FolderDrafts interface {
	SaveWaitingDraft(ctx context.Context, id core.RequestID, d WaitingDraft) (core.TaskID, error)
}

// WaitingDraft is the draft a diagnosis leaves for an unreadable repo.
type WaitingDraft struct {
	Project core.ProjectID
	Repo    core.RepoID
	Title   string
	Brief   string
	Agent   string
	Model   string
}

// ProposalSpec is one proposal to create: an already validated draft action
// with its summary. The action type is core/proposals (frozen contracts).
type ProposalSpec struct {
	// ID is the proposal ID the dispatch generated (core Dispatch.IDs).
	ID      core.ProposalID
	Task    core.TaskID
	Repo    core.RepoID
	Summary string
	// Kind and Payload are ProposalKind and the action's stored JSON (RowOf).
	Kind    core.ProposalKind
	Payload []byte
}

// RequestStore persists a request's durable intent, state and links, one
// Store.Commit per change (db.Store implements it).
type RequestStore interface {
	GetActionRequest(ctx context.Context, id core.RequestID) (db.ActionRecord, error)
	ListUnsettledActionRequests(ctx context.Context) ([]db.ActionRecord, error)
	Commit(ctx context.Context, fn func(*db.Tx) error) error
}

// ReviewCommentReader finds an earlier comment by its marker, for read-only
// reconciliation of a proposal whose POST outcome is unknown. An incomplete
// read is CommentUnknown, never proof of absence.
type ReviewCommentReader interface {
	FindReviewComment(ctx context.Context, repo core.RepoID, mr int, marker, bodyHash, expectedHead string) (coreproposals.CommentLookup, error)
}

// Deps is the aggregate the orchestration task builds the service from. The
// optional-service pattern of Phase 5 applies: the service is bound to the API
// with api.Server.SetPhase6Services after the server exists.
type Deps struct {
	Scope     ScopeReader
	Context   ContextReader
	Tasks     TaskSubmitter
	Proposals ProposalCreator
	Requests  RequestStore
	Comments  ReviewCommentReader
	Hub       api.Phase6Hub
	// Drafts is optional (see FolderDrafts).
	Drafts FolderDrafts
	// Redactor redacts every free-text field before it is attached, stored or
	// audited (invariant 4).
	Redactor safety.Redactor
	// Clock and IDs are the shell's time and randomness; the core never makes one.
	Clock func() time.Time
	IDs   func(prefix string) string
}

// *db.Store is the live RequestStore, and *api.Server the live hub.
var (
	_ RequestStore  = (*db.Store)(nil)
	_ api.Phase6Hub = (*api.Server)(nil)
)
