package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// This file is owned by the code search task (XR-01). Its design
// (docs/design/code-search.md) is written before that task starts and may
// reshape the interface below. The file is excluded from tygo (tygo.yaml).

// SearchService is implemented by internal/search. It searches only tracked
// repos (invariant 6). Every method returns errors classified by
// internal/apperr.
type SearchService interface {
	// Search looks for text in the given repos (empty means all tracked
	// repos) and returns at most limit hits.
	Search(ctx context.Context, text string, repos []core.RepoID, limit int) ([]SearchHit, error)
}

// SearchHit is one matching line.
type SearchHit struct {
	Repo core.RepoID
	Path string
	Line int
	Text string
}

// routesSearch registers the search endpoint. It registers nothing when the
// service is not wired.
func (s *Server) routesSearch(mux *http.ServeMux) {
	if s.search == nil {
		return
	}
	_ = mux // the code search task adds its routes here
}
