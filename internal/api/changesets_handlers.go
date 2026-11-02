package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The handlers of the change-set endpoints (RV-03, RV-04). The service parses
// the order, the scope and the hashes the user saw; the handlers only decode
// strictly, validate IDs and map errors.

func changeSetID(w http.ResponseWriter, r *http.Request) (core.ChangeSetID, bool) {
	id, ok := pathOtherID(w, r, "change set", func(s string) error { return core.ChangeSetID(s).Validate() })
	return core.ChangeSetID(id), ok
}

// csCall is the shape of the endpoints that take only a change-set ID.
func (s *Server) csCall(w http.ResponseWriter, r *http.Request, what string, call func(ChangeSetService, context.Context, core.ChangeSetID) (ChangeSetDTO, error)) {
	svc := s.phase4().ChangeSets
	if svc == nil {
		notReady(w, what)
		return
	}
	id, ok := changeSetID(w, r)
	if !ok {
		return
	}
	out, err := call(svc, r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeChangeSet(out))
}

func (s *Server) handleGetChangeSet(w http.ResponseWriter, r *http.Request) {
	s.csCall(w, r, "The change set", ChangeSetService.Get)
}

func (s *Server) handleStopChangeSet(w http.ResponseWriter, r *http.Request) {
	s.csCall(w, r, "Stopping a change-set run", ChangeSetService.StopRun)
}

func (s *Server) handleCloseChangeSet(w http.ResponseWriter, r *http.Request) {
	s.csCall(w, r, "Closing a change set", ChangeSetService.Close)
}

func (s *Server) handleTaskChangeSet(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().ChangeSets
	if svc == nil {
		notReady(w, "The change set of a task")
		return
	}
	task, ok := pathTaskID(w, r)
	if !ok {
		return
	}
	out, err := svc.ForTask(r.Context(), task)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeChangeSet(out))
}

func (s *Server) handleSetChangeSetOrder(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().ChangeSets
	if svc == nil {
		notReady(w, "Changing the merge order")
		return
	}
	id, ok := changeSetID(w, r)
	if !ok {
		return
	}
	var req SetOrderRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.SetOrder(r.Context(), id, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeChangeSet(out))
}

func (s *Server) handlePreflightChangeSet(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().ChangeSets
	if svc == nil {
		notReady(w, "The change-set check")
		return
	}
	id, ok := changeSetID(w, r)
	if !ok {
		return
	}
	var req PreflightRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.Preflight(r.Context(), id, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if out.Warnings == nil {
		out.Warnings = []WarningDTO{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleConfirmChangeSet(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().ChangeSets
	if svc == nil {
		notReady(w, "Confirming a change set")
		return
	}
	id, ok := changeSetID(w, r)
	if !ok {
		return
	}
	var req ConfirmRunRequest
	if !decodeBody(w, r, &req) {
		return
	}
	// A changed hash is a Conflict and nothing runs; the service returns at
	// once and the run continues in the background.
	out, err := svc.ConfirmRun(r.Context(), id, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeChangeSet(out))
}

// normalizeChangeSet makes the lists JSON arrays, never null.
func normalizeChangeSet(c ChangeSetDTO) ChangeSetDTO {
	if c.Repos == nil {
		c.Repos = []ChangeSetRepoDTO{}
	}
	if c.Order == nil {
		c.Order = []core.RepoID{}
	}
	if c.SuggestedOrder == nil {
		c.SuggestedOrder = []core.RepoID{}
	}
	if c.Members == nil {
		c.Members = []ChangeSetMemberDTO{}
	}
	if c.Links == nil {
		c.Links = []MRLinkDTO{}
	}
	if c.Warnings == nil {
		c.Warnings = []WarningDTO{}
	}
	return c
}
