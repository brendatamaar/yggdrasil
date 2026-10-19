package hosts

import (
	"context"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// HostClient reads one kind of git host. It only sends GET requests, through
// a Getter, and sends the token only in a header. Errors are *FetchError, or
// ErrNotModified when every request answered 304 for the ETag. A response the
// client cannot parse is returned as an ordinary error: the service treats it
// as a transient failure.
type HostClient interface {
	// RepoState returns the MR and pipeline of the branch, and the ETag to
	// send next time (empty if the host gave none). A branch without an MR
	// gives a RepoState with a nil MR; one without a pipeline, a nil Pipeline.
	RepoState(ctx context.Context, inst corehosts.Instance, token string, project corehosts.ProjectPath, branch, etag string) (corehosts.RepoState, string, error)
	// Inbox returns the signed-in user's open MRs: review requested, assigned
	// and authored. It needs a token.
	Inbox(ctx context.Context, inst corehosts.Instance, token string) ([]corehosts.InboxItem, error)
}
