package verify

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// TrustSource reports the live trust view of a repo (SF-09): the same view the
// admission guard reads. The owner scans the repo's agent config; a repo that
// ships none is guard.TrustNone.
type TrustSource interface {
	TrustView(ctx context.Context, repo core.RepoID) (guard.TrustView, error)
}

// repoTrusted is the admission predicate: the view is none (nothing to trust)
// or trusted. Anything else, and any error, is "not trusted" (fail closed; the
// caller decides whether an error is retried).
func (s *Service) repoTrusted(ctx context.Context, repo core.RepoID) (bool, error) {
	v, err := s.d.Trust.TrustView(ctx, repo)
	if err != nil {
		return false, err
	}
	return v.Admissible(), nil
}
