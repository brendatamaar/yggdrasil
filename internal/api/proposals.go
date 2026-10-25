package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// This file is owned by the proposals shell task (PR1; RV-02). The DTOs are in
// types_proposals.go. The file is excluded from tygo (tygo.yaml): it holds
// Go-only wiring.

// ProposalFilter selects proposals for GET /api/proposals. Empty fields match
// everything.
type ProposalFilter struct {
	Task   core.TaskID
	Status core.ProposalStatus
}

// ProposalService is implemented by internal/proposals
// (docs/design/proposals.md). It loads the state, calls the pure step, commits
// the state with its audit entry and runs the commands. There is no method
// that creates a proposal: agents create them through MCP, not through the
// HTTP API. Errors are classified by internal/apperr.
type ProposalService interface {
	List(ctx context.Context, f ProposalFilter) ([]ProposalView, error)
	Get(ctx context.Context, id core.ProposalID) (ProposalView, error)
	// Confirm confirms the exact action hash the user saw. A different hash is
	// a conflict and changes nothing.
	Confirm(ctx context.Context, id core.ProposalID, hash string) (ProposalView, error)
	Reject(ctx context.Context, id core.ProposalID) (ProposalView, error)
	// Retry re-runs a retryable failed proposal. Never automatic.
	Retry(ctx context.Context, id core.ProposalID) (ProposalView, error)
}

// ProposalView is a proposal with the names the UI needs resolved.
type ProposalView struct {
	Proposal coreproposals.Proposal
	RepoName string
}

// routesProposals registers the proposal endpoints.
func (s *Server) routesProposals(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/proposals", s.handleListProposals)
	mux.HandleFunc("GET /api/proposals/{id}", s.handleGetProposal)
	mux.HandleFunc("POST /api/proposals/{id}/confirm", s.handleConfirmProposal)
	mux.HandleFunc("POST /api/proposals/{id}/reject", s.handleRejectProposal)
	mux.HandleFunc("POST /api/proposals/{id}/retry", s.handleRetryProposal)
}

// The handlers are stubs until task PR1.

func (s *Server) handleListProposals(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The proposal list")
}

func (s *Server) handleGetProposal(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Proposals")
}

func (s *Server) handleConfirmProposal(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Confirming a proposal")
}

func (s *Server) handleRejectProposal(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Rejecting a proposal")
}

func (s *Server) handleRetryProposal(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Retrying a proposal")
}
