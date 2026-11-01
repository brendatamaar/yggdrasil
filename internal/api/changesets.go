package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The change-set endpoints (RV-03, RV-04). Phase 4 foundations: routes and
// service interface only; every handler answers 501 until AP1 replaces it. The
// DTOs are in types_changesets.go. The file is excluded from tygo (tygo.yaml).

// ChangeSetService is implemented by an adapter in cmd/ygg over
// internal/changesets (docs/design/changesets.md). A nil service answers 501.
//
// ConfirmRun confirms one proposal at a time through the proposals service with
// the hash the user saw; it never confirms a proposal outside the set. The plain
// /api/proposals/{id}/confirm route stays and does not check the order.
type ChangeSetService interface {
	Get(ctx context.Context, id core.ChangeSetID) (ChangeSetDTO, error)
	// ForTask returns the task's change set (one per task, created with its
	// first proposal); NotFound when the task has none yet.
	ForTask(ctx context.Context, task core.TaskID) (ChangeSetDTO, error)
	SetOrder(ctx context.Context, id core.ChangeSetID, req SetOrderRequest) (ChangeSetDTO, error)
	Preflight(ctx context.Context, id core.ChangeSetID, req PreflightRequest) (PreflightResponse, error)
	ConfirmRun(ctx context.Context, id core.ChangeSetID, req ConfirmRunRequest) (ChangeSetDTO, error)
	StopRun(ctx context.Context, id core.ChangeSetID) (ChangeSetDTO, error)
	Close(ctx context.Context, id core.ChangeSetID) (ChangeSetDTO, error)
}

// routesChangeSets registers the change-set endpoints, whether or not the
// service is wired.
func (s *Server) routesChangeSets(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/changesets/{id}", s.handleGetChangeSet)
	mux.HandleFunc("GET /api/tasks/{id}/changeset", s.handleTaskChangeSet)
	mux.HandleFunc("PUT /api/changesets/{id}/order", s.handleSetChangeSetOrder)
	mux.HandleFunc("POST /api/changesets/{id}/preflight", s.handlePreflightChangeSet)
	mux.HandleFunc("POST /api/changesets/{id}/confirm", s.handleConfirmChangeSet)
	mux.HandleFunc("POST /api/changesets/{id}/stop", s.handleStopChangeSet)
	mux.HandleFunc("POST /api/changesets/{id}/close", s.handleCloseChangeSet)
}

// TODO(AP1): handlers.

func (s *Server) handleGetChangeSet(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The change set")
}

func (s *Server) handleTaskChangeSet(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The change set of a task")
}

func (s *Server) handleSetChangeSetOrder(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Changing the merge order")
}

func (s *Server) handlePreflightChangeSet(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The change-set check")
}

func (s *Server) handleConfirmChangeSet(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Confirming a change set")
}

func (s *Server) handleStopChangeSet(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Stopping a change-set run")
}

func (s *Server) handleCloseChangeSet(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Closing a change set")
}
