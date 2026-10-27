package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

// This file is owned by the composer backend task (D1; AG-13, AG-20). The DTOs
// are in types_composer.go. The file is excluded from tygo (tygo.yaml): it
// holds Go-only wiring.

// ComposerService is implemented by internal/composer (*composer.Service).
// Mentions only name files of tracked repos (invariant 6). Errors are
// classified by internal/apperr.
type ComposerService interface {
	SavedPrompts(ctx context.Context) ([]SavedPromptDTO, error)
	CreatePrompt(ctx context.Context, name, text string) (SavedPromptDTO, error)
	UpdatePrompt(ctx context.Context, id, name, text string) (SavedPromptDTO, error)
	DeletePrompt(ctx context.Context, id string) error
	// Mentions fuzzy-searches files and folders of the project's tracked repos
	// and returns at most limit results.
	Mentions(ctx context.Context, project core.ProjectID, query string, limit int) (MentionsResponse, error)
	// Models lists the models of every registered agent.
	Models(ctx context.Context) ([]ModelsResponse, error)
	// Draft runs one read-only agent call and returns text for the user to
	// edit. It never writes to a repo.
	Draft(ctx context.Context, req DraftRequest) (DraftResponse, error)
}

// maxMentions is the most results one mentions request returns.
const maxMentions = 50

// routesComposer registers the composer endpoints.
func (s *Server) routesComposer(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/composer/prompts", s.handleListPrompts)
	mux.HandleFunc("POST /api/composer/prompts", s.handleSavePrompt)
	mux.HandleFunc("PUT /api/composer/prompts/{id}", s.handleUpdatePrompt)
	mux.HandleFunc("DELETE /api/composer/prompts/{id}", s.handleDeletePrompt)
	mux.HandleFunc("GET /api/composer/mentions", s.handleMentions)
	mux.HandleFunc("GET /api/composer/models", s.handleModels)
	mux.HandleFunc("POST /api/composer/drafts", s.handleDraft)
}

// composerSvc returns the service, or answers 501 when none is wired.
func (s *Server) composerSvc(w http.ResponseWriter, what string) (ComposerService, bool) {
	if s.composer == nil {
		notReady(w, what)
		return nil, false
	}
	return s.composer, true
}

func (s *Server) handleListPrompts(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.composerSvc(w, "Saved prompts")
	if !ok {
		return
	}
	list, err := svc.SavedPrompts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if list == nil {
		list = []SavedPromptDTO{}
	}
	writeJSON(w, http.StatusOK, SavedPromptsResponse{Prompts: list})
}

func (s *Server) handleSavePrompt(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.composerSvc(w, "Saving a prompt")
	if !ok {
		return
	}
	var req SavePromptRequest
	if !decodeBody(w, r, &req) {
		return
	}
	p, err := svc.CreatePrompt(r.Context(), req.Name, req.Text)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdatePrompt(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.composerSvc(w, "Saving a prompt")
	if !ok {
		return
	}
	var req UpdatePromptRequest
	if !decodeBody(w, r, &req) {
		return
	}
	p, err := svc.UpdatePrompt(r.Context(), r.PathValue("id"), req.Name, req.Text)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePrompt(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.composerSvc(w, "Deleting a saved prompt")
	if !ok {
		return
	}
	if err := svc.DeletePrompt(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMentions(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.composerSvc(w, "File mentions")
	if !ok {
		return
	}
	q := r.URL.Query()
	project := core.ProjectID(q.Get("project"))
	if err := project.Validate(); err != nil {
		badRequest(w, "That project ID isn't valid.", "")
		return
	}
	limit := maxMentions
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			badRequest(w, "The limit must be a positive number.", "")
			return
		}
		limit = min(n, maxMentions)
	}
	res, err := svc.Mentions(r.Context(), project, q.Get("q"), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if res.Mentions == nil {
		res.Mentions = []MentionDTO{}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.composerSvc(w, "The model list")
	if !ok {
		return
	}
	all, err := svc.Models(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := ModelsListResponse{Agents: []ModelsResponse{}}
	agent := r.URL.Query().Get("agent")
	for _, m := range all {
		if agent == "" || m.Agent == agent {
			out.Agents = append(out.Agents, m)
		}
	}
	if agent != "" && len(out.Agents) == 0 {
		s.fail(w, r, apperr.New(apperr.NotFound, "That agent isn't available.", nil))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.composerSvc(w, "Drafting text with an agent")
	if !ok {
		return
	}
	var req DraftRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := req.ProjectID.Validate(); err != nil {
		badRequest(w, "That project ID isn't valid.", "")
		return
	}
	if err := req.RepoID.Validate(); err != nil {
		badRequest(w, "That repo ID isn't valid.", "")
		return
	}
	if req.TaskID != "" {
		if err := req.TaskID.Validate(); err != nil {
			badRequest(w, "That task ID isn't valid.", "")
			return
		}
	}
	res, err := svc.Draft(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
