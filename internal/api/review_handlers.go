package api

import (
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core/review"
)

// The handlers of the review-note endpoints (RV-01). The service parses and
// validates the note (path, line, side, text, diff hash) and answers an apperr
// Invalid for what is wrong.

func (s *Server) handleListNotes(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Review
	if svc == nil {
		notReady(w, "Review notes")
		return
	}
	task, ok := pathTaskID(w, r)
	if !ok {
		return
	}
	out, err := svc.List(r.Context(), task)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if out.Notes == nil {
		out.Notes = []ReviewNoteDTO{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateNote(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Review
	if svc == nil {
		notReady(w, "Adding a review note")
		return
	}
	task, ok := pathTaskID(w, r)
	if !ok {
		return
	}
	var req CreateNoteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.Create(r.Context(), task, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func noteID(w http.ResponseWriter, r *http.Request) (review.NoteID, bool) {
	id, ok := pathOtherID(w, r, "note", func(s string) error { return review.NoteID(s).Validate() })
	return review.NoteID(id), ok
}

func (s *Server) handleEditNote(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Review
	if svc == nil {
		notReady(w, "Editing a review note")
		return
	}
	id, ok := noteID(w, r)
	if !ok {
		return
	}
	var req EditNoteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.Edit(r.Context(), id, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Review
	if svc == nil {
		notReady(w, "Deleting a review note")
		return
	}
	id, ok := noteID(w, r)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSendNotes(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Review
	if svc == nil {
		notReady(w, "Sending review notes to the agent")
		return
	}
	task, ok := pathTaskID(w, r)
	if !ok {
		return
	}
	var req SendNotesRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.Send(r.Context(), task, req)
	if err != nil {
		// A busy agent is a Conflict and the notes stay open.
		s.fail(w, r, err)
		return
	}
	if out.Notes == nil {
		out.Notes = []ReviewNoteDTO{}
	}
	writeJSON(w, http.StatusOK, out)
}
