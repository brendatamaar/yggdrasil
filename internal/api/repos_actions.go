package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// This file is the git actions (WS-06 to WS-08): fetch, pull, checkout, branch
// switch, open in editor and show in folder, plus the editor setting. A click
// runs directly with an audit entry; these are not proposals. The DTOs are in
// types_actions.go. The file is excluded from tygo (tygo.yaml).

// GitActionsService is implemented by repos.GitActions (the batch service and
// the open service together). Start fails with an apperr NotFound when a repo
// is not tracked, Unavailable when the audit log cannot be written.
type GitActionsService interface {
	Start(ctx context.Context, req repos.BatchRequest, actor corerepos.Actor) (corerepos.BatchID, error)
	Get(id corerepos.BatchID) (repos.BatchView, bool)
	Cancel(id corerepos.BatchID, actor corerepos.Actor) (repos.BatchView, error)
	Open(ctx context.Context, id core.RepoID, req repos.OpenRequest) error
	Reveal(ctx context.Context, id core.RepoID, file repos.RelPath) error
	Editor(ctx context.Context) (repos.EditorSetting, error)
	SetEditor(ctx context.Context, e repos.Editor, actor corerepos.Actor) error
}

// routesReposActions registers nothing when the service is not wired.
func (s *Server) routesReposActions(mux *http.ServeMux) {
	if s.gitActions == nil {
		return
	}
	mux.HandleFunc("POST /api/git/batches", s.startGitBatch)
	mux.HandleFunc("GET /api/git/batches/{id}", s.getGitBatch)
	mux.HandleFunc("POST /api/git/batches/{id}/cancel", s.cancelGitBatch)
	mux.HandleFunc("POST /api/repos/{id}/open", s.openRepo)
	mux.HandleFunc("POST /api/repos/{id}/reveal", s.revealRepo)
	mux.HandleFunc("GET /api/settings/editor", s.getEditor)
	mux.HandleFunc("PUT /api/settings/editor", s.putEditor)
}

// PublishGitBatch tells the repos topic and the topic of every repo of the
// batch that it changed. *repos.ActionsService calls it.
func (s *Server) PublishGitBatch(batch corerepos.BatchID, repoIDs []core.RepoID, finished bool) {
	msg := GitBatchChangedMessage{BatchID: string(batch), Finished: finished}
	s.hub.publishNext(reposTopic{}, msg)
	for _, id := range repoIDs {
		s.hub.publishNext(repoTopic{ID: id}, msg)
	}
}

func gitBatchDTO(v repos.BatchView) GitBatchDTO {
	d := GitBatchDTO{ID: string(v.ID), Kind: string(v.Kind), Branch: v.Branch, StartedAt: v.Started,
		Finished: v.Finished, Items: make([]GitBatchItemDTO, 0, len(v.Items))}
	for _, it := range v.Items {
		i := GitBatchItemDTO{RepoID: string(it.Repo), State: it.State, Outcome: it.Outcome, Reason: it.Reason,
			Message: it.Message, From: it.From, To: it.To, Branch: it.Branch, Created: it.Created}
		if !it.At.IsZero() {
			at := it.At
			i.At = &at
		}
		d.Items = append(d.Items, i)
	}
	return d
}

func (s *Server) startGitBatch(w http.ResponseWriter, r *http.Request) {
	var body StartGitBatchRequest
	if !decodeBody(w, r, &body) {
		return
	}
	req, err := repos.ParseBatchRequest(body.Kind, body.Repos, body.Branch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id, err := s.gitActions.Start(r.Context(), req, "user")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, StartGitBatchResponse{BatchID: string(id)})
}

func (s *Server) gitBatchID(w http.ResponseWriter, r *http.Request) (corerepos.BatchID, bool) {
	id := r.PathValue("id")
	if id == "" || len(id) > 64 {
		writeError(w, http.StatusNotFound, ErrNotFound, "That batch is gone.", "Start the action again.")
		return "", false
	}
	return corerepos.BatchID(id), true
}

func (s *Server) getGitBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := s.gitBatchID(w, r)
	if !ok {
		return
	}
	v, found := s.gitActions.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, ErrNotFound, "That batch is gone. Finished batches are kept for 10 minutes.", "Check the repo status instead.")
		return
	}
	writeJSON(w, http.StatusOK, gitBatchDTO(v))
}

func (s *Server) cancelGitBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := s.gitBatchID(w, r)
	if !ok {
		return
	}
	v, err := s.gitActions.Cancel(id, "user")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gitBatchDTO(v))
}

// decodeOptional reads a JSON body, treating a missing body as {}.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return true
	}
	return decodeBody(w, r, v)
}

func (s *Server) openRepo(w http.ResponseWriter, r *http.Request) {
	id, ok := s.detailRepoID(w, r)
	if !ok {
		return
	}
	var body OpenRequestBody
	if !decodeOptional(w, r, &body) {
		return
	}
	req, err := repos.ParseOpen(body.Editor, body.File)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.gitActions.Open(r.Context(), id, req); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revealRepo(w http.ResponseWriter, r *http.Request) {
	id, ok := s.detailRepoID(w, r)
	if !ok {
		return
	}
	var body RevealRequestBody
	if !decodeOptional(w, r, &body) {
		return
	}
	req, err := repos.ParseOpen("", body.File)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.gitActions.Reveal(r.Context(), id, req.File); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getEditor(w http.ResponseWriter, r *http.Request) {
	set, err := s.gitActions.Editor(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	dto := EditorSettingDTO{Editor: string(set.Chosen), Detected: make([]string, 0, len(set.Detected))}
	for _, e := range set.Detected {
		dto.Detected = append(dto.Detected, string(e))
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) putEditor(w http.ResponseWriter, r *http.Request) {
	var body SetEditorRequest
	if !decodeBody(w, r, &body) {
		return
	}
	e, err := repos.ParseEditor(body.Editor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.gitActions.SetEditor(r.Context(), e, "user"); err != nil {
		s.fail(w, r, err)
		return
	}
	s.getEditor(w, r)
}
