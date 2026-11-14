package hosts

// Capability declarations of the Phase 6 host work (docs/design/
// phase-6-host-context.md and phase-6-actions.md). Declarations only: the
// implementations come with the host-context task (H). Two levels:
//
//   - ContextClient and WriteClient are optional capabilities of a HostClient
//     implementation (a provider package). They take the token as an argument
//     like HostClient does, and send it only in a header.
//   - ContextReader, CommentHost and RepoHost are what the Service exposes to
//     the rest of the program. They take no token: the token never leaves this
//     package, so the actions and proposals packages cannot misuse one.
//
// Every read is bounded (corehosts.Context* constants) and returns redacted
// text. Writes exist ONLY for confirmed proposal execution; they are not
// reachable from an agent or an API handler.

import (
	"context"
	"net/http"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// ContextResult is a successful context read: the payload, the labelled gaps and
// when it stops being fresh.
type ContextResult struct {
	Value       corehosts.ContextPayload
	Unavailable []corehosts.ContextUnavailable
	Expires     time.Time
}

// ContextClient is implemented by provider clients that can read context.
// Errors are *FetchError (classified) like HostClient's. A provider that cannot
// give a kind returns ErrContextUnsupported for it.
type ContextClient interface {
	// FailedJobs lists the failed jobs of run runID and their logs. head is the
	// commit the caller believes the run built ("" when unknown).
	FailedJobs(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, runID, head string) (ContextResult, error)
	// Issues lists open issues, bounded to corehosts.ContextMaxEntries.
	Issues(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath) (ContextResult, error)
	// Issue returns one issue.
	Issue(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, number int) (ContextResult, error)
	// MRDiff returns the merge request, its immutable head and its diff.
	MRDiff(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, number int) (ContextResult, error)
}

// ContextReader reads host context for a TRACKED repo. It resolves the repo to
// its instance and project, enforces tracked-and-ready scope and ownership,
// runs corehosts.ContextStep and the bounded fetches, and redacts. Key is a
// validated corehosts.ContextKey; a key for an untracked or reserved repo is
// db.ErrNotFound-equivalent (ErrContextScope).
type ContextReader interface {
	Read(ctx context.Context, key corehosts.ContextKey) (ContextOutcome, error)
}

// ContextOutcome is the answer of ContextReader: exactly one of Result (a Ready
// or Partial value) or Blocked is set. A blocked read is an answer, not an
// error; it carries the plain reason so callers can degrade to metadata.
type ContextOutcome struct {
	Result  *ContextResult
	Blocked *corehosts.CtxBlocked
	// RetryAt is set when the read is waiting for a retry.
	RetryAt time.Time
}

// ErrContextScope is returned when the key's repo is not tracked and ready, is
// not linked to a configured instance, or the object does not belong to it.
var ErrContextScope = errContext("host context: the repo is not tracked or has no host")

// ErrContextUnsupported is returned by a ContextClient for a kind the provider
// cannot give (Gitea logs, GitHub external checks).
var ErrContextUnsupported = errContext("host context: this host cannot give that")

type errContext string

func (e errContext) Error() string { return string(e) }

// CommentHost writes and finds review comments for proposal execution (AC-02).
// The posted text is Body followed by the marker line; the host service builds
// it, so the marker cannot be omitted. PostReviewComment sends one POST and no
// retry: a timeout or lost reply is reported as an unknown outcome
// (ErrOutcomeUnknown) and reconciled with FindReviewComment before a human
// retry. MRHead rechecks the head just before posting.
type CommentHost interface {
	MRHead(ctx context.Context, repo core.RepoID, mr int) (string, error)
	PostReviewComment(ctx context.Context, repo core.RepoID, mr int, expectedHead, body, marker string) (commentID string, err error)
	FindReviewComment(ctx context.Context, repo core.RepoID, mr int, marker, bodyHash, expectedHead string) (coreproposals.CommentLookup, error)
}

// ErrOutcomeUnknown marks a mutating host call whose result is unknown
// (timeout, cancellation, lost reply). The caller must reconcile read-only and
// must not resend blindly.
var ErrOutcomeUnknown = errContext("host write: the outcome is unknown")

// RepoOwnerInfo is what the host says about an owner account.
type RepoOwnerInfo struct {
	// Kind is "user" or "org".
	Kind string
	// CanCreate: the token can create repositories under the owner.
	CanCreate bool
}

// RepoLookup is the answer of LookupRepo (read-only reconciliation of a
// created remote). Adoption requires an exact marker, owner, visibility and an
// empty ref state; anything else is Conflict, never adopted.
type RepoLookup struct {
	Outcome RepoLookupOutcome
	// RemoteID is the host's immutable repository ID when found.
	RemoteID string
	// URL is the credential-free HTTPS clone URL.
	URL string
}

// RepoLookupOutcome is a closed set.
type RepoLookupOutcome string

const (
	RepoExactMatch RepoLookupOutcome = "exact_match"
	RepoAbsent     RepoLookupOutcome = "absent"
	RepoConflict   RepoLookupOutcome = "conflict"
	RepoUnknown    RepoLookupOutcome = "unknown"
)

// CreatedRepo is the reply of CreateRepo.
type CreatedRepo struct {
	RemoteID string
	URL      string
}

// CreateRepoSpec is the immutable input of CreateRepo. Description carries the
// operation marker; Private is true unless the user chose public.
type CreateRepoSpec struct {
	Instance    corehosts.InstanceID
	Owner       string
	OwnerKind   string
	Name        string
	Description string
	Private     bool
}

// RepoHost creates and finds remote repositories for the confirmed publish
// stages (NR-03/04), GitHub only at first. CreateRepo is one POST with no
// retry (unknown outcome => ErrOutcomeUnknown). LookupRepo is the read-only
// reconciliation. ResolveOwner validates the account type on the server.
type RepoHost interface {
	ResolveOwner(ctx context.Context, inst corehosts.InstanceID, owner string) (RepoOwnerInfo, error)
	CreateRepo(ctx context.Context, spec CreateRepoSpec) (CreatedRepo, error)
	LookupRepo(ctx context.Context, spec CreateRepoSpec) (RepoLookup, error)
}

// PushAuthorizer lets the in-process publish transport add the host token as an
// HTTP header to its own requests, so the token is never given to a child
// process, URL, config or environment (invariant 3). Authorize may only set the
// Authorization header of requests to the instance's own configured HTTPS
// clone origin. The destination is supplied on every call so implementations
// can reject foreign origins, userinfo and unsafe URLs before adding credentials.
// It must not change the destination, method, body or other headers. The
// transport must also refuse redirects to another origin.
type PushAuthorizer interface {
	Authorize(ctx context.Context, inst corehosts.InstanceID, req *http.Request) error
}
