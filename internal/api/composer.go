package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// This file is owned by the composer backend task (D1; AG-13, AG-20). The DTOs
// are in types_composer.go. The file is excluded from tygo (tygo.yaml): it
// holds Go-only wiring.

// ComposerService is implemented by internal/composer. Mentions only name
// files of tracked repos (invariant 6). Errors are classified by
// internal/apperr.
type ComposerService interface {
	SavedPrompts(ctx context.Context) ([]SavedPromptDTO, error)
	SavePrompt(ctx context.Context, name, text string) (SavedPromptDTO, error)
	DeletePrompt(ctx context.Context, id string) error
	// Mentions fuzzy-searches files and folders of the project's tracked repos.
	Mentions(ctx context.Context, project core.ProjectID, query string) (MentionsResponse, error)
	// Models lists the models an agent offers.
	Models(ctx context.Context, agent string) ([]ModelDTO, error)
	// Draft runs one read-only agent call and returns text for the user to edit.
	Draft(ctx context.Context, req DraftRequest) (string, error)
}

// routesComposer registers the composer endpoints.
func (s *Server) routesComposer(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/composer/prompts", s.handleListPrompts)
	mux.HandleFunc("POST /api/composer/prompts", s.handleSavePrompt)
	mux.HandleFunc("DELETE /api/composer/prompts/{id}", s.handleDeletePrompt)
	mux.HandleFunc("GET /api/composer/mentions", s.handleMentions)
	mux.HandleFunc("GET /api/composer/models", s.handleModels)
	mux.HandleFunc("POST /api/composer/drafts", s.handleDraft)
}

// The handlers are stubs until task D1.

func (s *Server) handleListPrompts(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Saved prompts")
}

func (s *Server) handleSavePrompt(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Saving a prompt")
}

func (s *Server) handleDeletePrompt(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Deleting a saved prompt")
}

func (s *Server) handleMentions(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "File mentions")
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The model list")
}

func (s *Server) handleDraft(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Drafting text with an agent")
}
