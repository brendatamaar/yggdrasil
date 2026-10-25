package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// This file is owned by the safety shell and tasks service tasks (S2, S1;
// SF-09, D17). The DTOs are in types_trust.go. The file is excluded from tygo
// (tygo.yaml): it holds Go-only wiring.

// TrustService is implemented by internal/safety with the tasks service
// (docs/design/guard.md). It scans the repo's agent config, runs
// guard.StepTrust and commits the decision with its audit entry. Errors are
// classified by internal/apperr.
type TrustService interface {
	// Status scans the repo and returns its trust state.
	Status(ctx context.Context, repo core.RepoID) (guard.Trust, error)
	// Trust records trust in the exact hash the user was shown. A hash that is
	// no longer the repo's current config is a conflict.
	Trust(ctx context.Context, repo core.RepoID, hash string) (guard.Trust, error)
	// Revoke withdraws trust.
	Revoke(ctx context.Context, repo core.RepoID) (guard.Trust, error)
}

// routesTrust registers the trust endpoints.
func (s *Server) routesTrust(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{id}/trust", s.handleGetTrust)
	mux.HandleFunc("POST /api/repos/{id}/trust", s.handleSetTrust)
	mux.HandleFunc("DELETE /api/repos/{id}/trust", s.handleRevokeTrust)
}

// The handlers are stubs until the trust task (S2/S1).

func (s *Server) handleGetTrust(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Repo trust")
}

func (s *Server) handleSetTrust(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Trusting a repo")
}

func (s *Server) handleRevokeTrust(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Revoking trust in a repo")
}
