package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The landing endpoints (RV-05, RV-06): create a merge_local or remove_worktree
// proposal. Phase 4 foundations: routes and service interface only; every
// handler answers 501 until AP1 replaces it. The DTOs are in types_landing.go.
// The file is excluded from tygo (tygo.yaml).
//
// Neither route merges or removes anything. Both create a proposal that the
// user must confirm through /api/proposals/{id}/confirm (docs/AGENTS.md
// section 6, invariant 2). MCP is not offered either kind.

// LandingService is implemented by an adapter in cmd/ygg over
// internal/proposals. A nil service answers 501.
type LandingService interface {
	// ProposeMergeLocal creates a merge_local proposal for the worktree's
	// branch, with the repo, target branch and branch tip read by the service.
	ProposeMergeLocal(ctx context.Context, worktree core.WorktreeID, req MergeLocalRequest) (ProposalDTO, error)
	// ProposeRemoveWorktree creates a remove_worktree proposal.
	ProposeRemoveWorktree(ctx context.Context, worktree core.WorktreeID) (ProposalDTO, error)
}

// routesLanding registers the landing endpoints, whether or not the service is
// wired.
func (s *Server) routesLanding(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/worktrees/{id}/merge-local", s.handleProposeMergeLocal)
	mux.HandleFunc("POST /api/worktrees/{id}/remove", s.handleProposeRemoveWorktree)
}

// TODO(AP1): handlers.

func (s *Server) handleProposeMergeLocal(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Merging a branch into the main checkout")
}

func (s *Server) handleProposeRemoveWorktree(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Removing a worktree")
}
