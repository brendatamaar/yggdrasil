package proposals

// Capability declarations of the Phase 6 proposal executors
// (docs/design/phase-6-*.md). core/proposals defines the frozen actions
// (IgnoreEntries, ReviewComment, InitRepo, PublishRepo, PullFF); the executors
// in this package run them against these ports. Executors write only after a
// confirmation matching the action hash,
// revalidate scope, content hash and HEAD or MR head at execution, record a
// durable effect (db.EffectRecord) BEFORE every external write, and never
// retry an unknown outcome blindly.

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// IgnoreWriter is the guarded .gitignore append (platform.IgnoreWriter).
type IgnoreWriter = platform.IgnoreWriter

// CommentHost is the host's confirmed review-comment write and its read-only
// reconciliation (implemented by hosts.Service; internal/hosts may not be
// imported here, so the method set is declared where it is used). The token
// stays inside the host service; the executor never sees one. PostReviewComment
// sends one POST and never retries; a timeout or lost reply is reported as an
// error wrapping hosts.ErrOutcomeUnknown, which the executor reconciles with
// FindReviewComment before any human retry.
type CommentHost interface {
	MRHead(ctx context.Context, repo core.RepoID, mr int) (string, error)
	PostReviewComment(ctx context.Context, repo core.RepoID, mr int, expectedHead, body, marker string) (commentID string, err error)
	FindReviewComment(ctx context.Context, repo core.RepoID, mr int, marker, bodyHash, expectedHead string) (coreproposals.CommentLookup, error)
}

// NewRepoLifecycle is what the InitRepo and PublishRepo executors call into
// internal/newrepos. Confirm* returns once the durable worker owns the
// operation; progress and results come back through the operation's stored
// state (the executor reads it from the reservation row) and through
// Service.OperationChanged, which the wiring calls on every state change of an
// operation, not through this call. The proposal stays Confirmed until then.
//
// The hash is the immutable spec hash of the lifecycle itself (newrepos
// InitSpec.Hash for ConfirmInit, PublishSpec.Hash for ConfirmPublish), computed
// by the executor from the proposal's action after it checked that the action
// equals the reserved spec. attempt is strictly greater than every attempt the
// operation has seen. A PublishRepo proposal is linked to its operation by the
// hand-off effect record (db.EffectRecord with Operation set and no Stage), so
// the lifecycle can find the confirmed action.
type NewRepoLifecycle interface {
	// ConfirmInit starts initialization of the reserved repo of op after the user
	// confirmed the spec with hash. A hash that does not match the operation's
	// immutable spec is refused. A refusal that started nothing says so with an
	// error that has NothingWritten() true (or wraps ErrWriteRefused); any other
	// error is treated as an unknown outcome and only reconciled.
	ConfirmInit(ctx context.Context, op core.OperationID, hash string, attempt int) error
	// ConfirmPublish starts publishing a ready repo after the user confirmed the
	// PublishRepo hash.
	ConfirmPublish(ctx context.Context, op core.OperationID, hash string, attempt int) error
	// ReconcileOperation runs the read-only reconciliation of an operation left
	// Initializing, Publishing or Uncertain by a crash.
	ReconcileOperation(ctx context.Context, op core.OperationID) error
}

// PullRunner performs a confirmed fast-forward pull of a repo's main checkout
// through the existing git-action service, under the shared main lock, with no
// merge fallback (PullFF). It returns the existing pull outcome codes; Verify
// requests the fast verification chain afterwards.
type PullRunner interface {
	PullFF(ctx context.Context, repo core.RepoID, branch, upstream, expectHead string, verify bool) (PullFFResult, error)
}

// PullFFResult is the plain-language outcome of a pull.
type PullFFResult struct {
	// NewHead is the commit after a successful fast-forward ("" when nothing
	// changed). Note explains a refusal or "already up to date".
	NewHead string
	Note    string
	// BuildID is set when a verification build was started.
	BuildID core.BuildID
}

// PublishTransport is the in-process authenticated push (git.PublishTransport).
type PublishTransport = git.PublishTransport
