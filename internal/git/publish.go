package git

// The publish transport and the local helpers new-repo initialization and
// publishing need (NR-01 to NR-04, docs/design/phase-6-newrepos.md). This file
// declares the capabilities and their value types only; the new-repo task (N)
// implements them.
//
// Transport decision: authenticated pushes use an IN-PROCESS smart-HTTP git
// transport (github.com/go-git/go-git/v5, pinned in go.mod, Apache-2.0, pure
// Go), never a git child process, so no token reaches a child's environment,
// argv, config, credential helper or askpass. The token is added to the
// transport's own HTTP requests as a header by an Authorizer. The refspec is
// the single explicit "refs/heads/<branch>:refs/heads/<branch>" with no force
// and no ref deletion. Anything go-git cannot do safely (an unsupported object
// or storage format) is a clear refusal, never a fallback to a credential path.
// Other git operations keep using platform.Start with the clean environment.

import (
	"context"
	"net/http"
)

// Authorizer adds credentials to the transport's own HTTP requests. It may set
// only the Authorization header and only for the instance's own origin;
// implementations must refuse any other host. The transport calls it per
// request and never logs, stores or forwards what it sets; redirects to another
// authority are refused rather than followed with credentials.
type Authorizer interface {
	Authorize(ctx context.Context, req *http.Request) error
}

// RemoteRef is the state of one remote branch.
type RemoteRef struct {
	// Exists is false when the branch (or the whole remote) has no such ref.
	Exists bool
	// Head is the commit the branch points at when Exists.
	Head string
}

// PublishRequest is one push. URL is the credential-free HTTPS URL built from
// the configured host and a validated owner and name; it must carry no userinfo
// (the transport refuses one). ExpectedHead is the local commit to push; the
// push is refused if HEAD of the branch is not exactly it.
type PublishRequest struct {
	// Dir is the repo's working directory (the canonical path, no symlink).
	Dir          string
	URL          string
	Branch       string
	ExpectedHead string
}

// PublishResult is what the remote holds after a publish push.
type PublishResult struct {
	Head string
}

// PublishTransport reads and pushes through the in-process transport.
// RemoteBranch is the read-only reconciliation: it lists the remote ref without
// sending any object. PublishBranch sends the single branch, never forces and never
// deletes; a remote ref that exists with a different head is a rejection
// (ErrRemoteDiverged), not an overwrite. A lost reply is ErrPublishUnknown and is
// reconciled with RemoteBranch before any retry.
type PublishTransport interface {
	RemoteBranch(ctx context.Context, req PublishRequest) (RemoteRef, error)
	PublishBranch(ctx context.Context, req PublishRequest) (PublishResult, error)
}

// Sentinel errors of the publish transport.
var (
	// ErrRemoteDiverged: the remote branch exists with another head.
	ErrRemoteDiverged = publishErr("the remote branch already has different commits")
	// ErrPublishUnknown: the publish may or may not have reached the remote.
	ErrPublishUnknown = publishErr("the publish result is unknown")
	// ErrUnsupportedFormat: the repository uses an object or storage format the
	// in-process transport cannot publish safely.
	ErrUnsupportedFormat = publishErr("this repository cannot be published from here")
)

type publishErr string

func (e publishErr) Error() string { return string(e) }

// OriginManager adds and reads the origin remote of a new repo. AddOrigin only
// succeeds while origin is absent (or already identical, for a resumed stage);
// it never replaces an existing origin. url must be credential-free.
type OriginManager interface {
	Origin(ctx context.Context, dir string) (url string, exists bool, err error)
	AddOrigin(ctx context.Context, dir, url string) error
}

// FirstCommit is the exact first commit of a new repo, for verification after
// an unknown outcome: the tree, message hash, author and the absence of a parent.
type FirstCommit struct {
	Commit      string
	Tree        string
	MessageHash string
	Author      string
}

// RepoInitializer runs the local git stages through the safe wrapper (clean
// environment, hooks disabled, platform.Start). InitRepo runs `git init` on an
// existing empty directory with an initial branch. CommitFirst commits exactly
// the listed files with the given message; it fails with the existing
// ErrNoAuthor before any write when no author is configured. ReadFirstCommit verifies an earlier commit
// without writing.
type RepoInitializer interface {
	InitRepo(ctx context.Context, dir, branch string) error
	CommitFirst(ctx context.Context, dir string, files []string, message string) (FirstCommit, error)
	ReadFirstCommit(ctx context.Context, dir, branch string) (FirstCommit, bool, error)
	HasAuthor(ctx context.Context, dir string) (bool, error)
}
