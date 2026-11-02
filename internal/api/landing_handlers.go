package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The handlers of the landing endpoints (RV-05, RV-06). They only CREATE a
// pending proposal; nothing is merged or removed until the user confirms it
// through /api/proposals/{id}/confirm (docs/AGENTS.md section 6, invariant 2).

func worktreeID(w http.ResponseWriter, r *http.Request) (core.WorktreeID, bool) {
	id, ok := pathOtherID(w, r, "worktree", func(s string) error { return core.WorktreeID(s).Validate() })
	return core.WorktreeID(id), ok
}

// maxMergeTitleRunes bounds the merge commit subject a client may suggest.
const maxMergeTitleRunes = 200

func (s *Server) handleProposeMergeLocal(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Landing
	if svc == nil {
		notReady(w, "Merging a branch into the main checkout")
		return
	}
	id, ok := worktreeID(w, r)
	if !ok {
		return
	}
	var req MergeLocalRequest
	if r.ContentLength != 0 { // the body is optional
		if !decodeBody(w, r, &req) {
			return
		}
	}
	if utf8.RuneCountInString(req.Title) > maxMergeTitleRunes || strings.ContainsFunc(req.Title, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		badRequest(w, "The merge title must be one line of at most 200 characters.", "")
		return
	}
	out, err := svc.ProposeMergeLocal(r.Context(), id, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleProposeRemoveWorktree(w http.ResponseWriter, r *http.Request) {
	svc := s.phase4().Landing
	if svc == nil {
		notReady(w, "Removing a worktree")
		return
	}
	id, ok := worktreeID(w, r)
	if !ok {
		return
	}
	out, err := svc.ProposeRemoveWorktree(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
