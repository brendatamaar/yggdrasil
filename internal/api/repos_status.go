package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

// This file is owned by the repo-status task (WS-02 to WS-04): the status
// service interface, GET /api/repos (the joined read model of projects,
// status and failed pipelines) and POST /api/repos/refresh. The DTOs are in
// types_repos.go. The file is excluded from tygo (tygo.yaml).

// StatusService is implemented by internal/repos (docs/design/repo-status.md).
// The status is an in-memory read model.
type StatusService interface {
	// Entries returns a copy of the current status of every tracked repo.
	Entries(ctx context.Context) (map[core.RepoID]corerepos.Entry, error)
	// Refresh asks for a new read of the repos; empty ids means all.
	Refresh(ctx context.Context, ids []core.RepoID, reason corerepos.RefreshReason) error
}

// routesReposStatus registers the status endpoints. It registers nothing
// when the status service is not wired.
func (s *Server) routesReposStatus(mux *http.ServeMux) {
	if s.status == nil {
		return
	}
	_ = mux // the repo-status task adds its routes here
}
