package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
)

// The review-note endpoints (RV-01). Phase 4 foundations: routes and service
// interface only; every handler answers 501 until AP1 replaces it. The DTOs are
// in types_review.go. The file is excluded from tygo (tygo.yaml).

// ReviewService is implemented by an adapter in cmd/ygg over
// internal/changesets (docs/design/changesets.md). A nil service answers 501.
type ReviewService interface {
	List(ctx context.Context, task core.TaskID) (ReviewNotesResponse, error)
	Create(ctx context.Context, task core.TaskID, req CreateNoteRequest) (ReviewNoteDTO, error)
	Edit(ctx context.Context, id review.NoteID, req EditNoteRequest) (ReviewNoteDTO, error)
	Delete(ctx context.Context, id review.NoteID) error
	// Send sends the notes to the agent as one message. A busy agent is a
	// conflict and the notes stay open.
	Send(ctx context.Context, task core.TaskID, req SendNotesRequest) (SendNotesResponse, error)
}

// routesReview registers the review-note endpoints, whether or not the service
// is wired.
func (s *Server) routesReview(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks/{id}/notes", s.handleListNotes)
	mux.HandleFunc("POST /api/tasks/{id}/notes", s.handleCreateNote)
	mux.HandleFunc("POST /api/tasks/{id}/notes/send", s.handleSendNotes)
	mux.HandleFunc("PUT /api/notes/{id}", s.handleEditNote)
	mux.HandleFunc("DELETE /api/notes/{id}", s.handleDeleteNote)
}
