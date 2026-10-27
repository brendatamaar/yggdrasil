package api

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
)

// This file is owned by the proposals shell task (PR1; RV-02). The DTOs are in
// types_proposals.go. The file is excluded from tygo (tygo.yaml): it holds
// Go-only wiring.

// ProposalService is implemented by *proposals.Service (docs/design/proposals.md).
// It loads the state, calls the pure step, commits the state with its audit
// entry and runs the commands. There is no Create: agents create proposals
// through MCP, never through this API. by names who decided ("user").
//
// Errors the handlers translate: *proposals.Refusal (409), ErrAlreadyDecided
// (409), db.ErrNotFound (404), ErrNotStarted (503); anything else is a generic
// 500 with the details only in the log.
type ProposalService interface {
	List(ctx context.Context, f proposals.ListFilter) ([]proposals.View, error)
	Get(ctx context.Context, id core.ProposalID) (proposals.View, error)
	// Confirm confirms the exact action hash the user saw. A different hash is
	// a conflict and changes nothing.
	Confirm(ctx context.Context, id core.ProposalID, hash, by string) (proposals.View, error)
	Reject(ctx context.Context, id core.ProposalID, by string) (proposals.View, error)
	// Retry re-runs a retryable failed proposal. Never automatic.
	Retry(ctx context.Context, id core.ProposalID, by string) (proposals.View, error)
}

// decider is the "by" recorded in the audit log for a decision made through
// this API. The API is the browser's: agents have no route that decides.
const decider = "user"

// routesProposals registers the proposal endpoints. Confirm, reject and retry
// are UI-only: they sit behind the Host/Origin guard like every /api route and
// nothing under /mcp reaches them.
func (s *Server) routesProposals(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/proposals", s.handleListProposals)
	mux.HandleFunc("GET /api/proposals/{id}", s.handleGetProposal)
	mux.HandleFunc("POST /api/proposals/{id}/confirm", s.handleConfirmProposal)
	mux.HandleFunc("POST /api/proposals/{id}/reject", s.handleRejectProposal)
	mux.HandleFunc("POST /api/proposals/{id}/retry", s.handleRetryProposal)
}

// proposalErr translates the proposals service's errors into an apperr class.
func proposalErr(err error) error {
	var ref *proposals.Refusal
	switch {
	case errors.As(err, &ref):
		msg := ref.Message
		if ref.Reason == coreprop.RefuseHashMismatch {
			msg = "What you were shown changed. Reload the page and review the proposal again."
		}
		return apperr.New(apperr.Conflict, msg, err)
	case errors.Is(err, proposals.ErrAlreadyDecided):
		return apperr.New(apperr.Conflict, "This proposal was already decided. Reload the page to see where it stands.", err)
	case errors.Is(err, db.ErrNotFound):
		return apperr.New(apperr.NotFound, "That proposal doesn't exist (any more).", err)
	case errors.Is(err, proposals.ErrNotStarted):
		return apperr.New(apperr.Unavailable, "Yggdrasil is starting or shutting down. Try again in a moment.", err)
	}
	return err
}

// proposalDTO converts a view. Summary stays plain text: the UI must not
// render it as markup or HTML.
func proposalDTO(v proposals.View) ProposalDTO {
	src := "user"
	if v.FromAgent {
		src = "agent"
	}
	d := ProposalDTO{
		ID: v.ID, ChangeSetID: v.ChangeSet, TaskID: v.Task, RepoID: v.Repo, Kind: v.Kind, Status: v.Status,
		Source: src, FromAgent: v.FromAgent, Summary: v.Summary, Lines: append([]string{}, v.Lines...),
		ActionHash: v.ActionHash, Attempt: v.Attempt, FailKind: v.FailKind, Message: v.Message,
		Retryable: v.Retryable, CreatedAt: v.CreatedAt, DecidedAt: v.DecidedAt,
	}
	if r := v.Result; r != nil {
		out := &ProposalResultDTO{CommitID: r.CommitID, MRURL: r.MRURL, BuildID: r.BuildID, Note: r.Note}
		for _, p := range r.MovedPaths {
			out.MovedPaths = append(out.MovedPaths, string(p))
		}
		d.Result = out
	}
	return d
}

func (s *Server) handleListProposals(w http.ResponseWriter, r *http.Request) {
	svc := s.proposals
	if svc == nil {
		notReady(w, "The proposal list")
		return
	}
	var f proposals.ListFilter
	q := r.URL.Query()
	if t := q.Get("task"); t != "" {
		f.Task = core.TaskID(t)
		if err := f.Task.Validate(); err != nil {
			badRequest(w, "That task ID isn't valid.", "")
			return
		}
	}
	if st := q.Get("status"); st != "" {
		f.Status = core.ProposalStatus(st)
		if err := f.Status.Validate(); err != nil {
			badRequest(w, "That status isn't one of pending, confirmed, rejected, executed or failed.", "")
			return
		}
	}
	if f.Task == "" && f.Status == "" {
		badRequest(w, "Say which proposals you want: give a task or a status.", "Add ?task=<id> or ?status=pending.")
		return
	}
	views, err := svc.List(r.Context(), f)
	if err != nil {
		s.fail(w, r, proposalErr(err))
		return
	}
	out := ProposalsResponse{Proposals: make([]ProposalDTO, 0, len(views))}
	for _, v := range views {
		out.Proposals = append(out.Proposals, proposalDTO(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// proposalID reads and validates the {id} path value; it answers 404 itself.
func proposalID(w http.ResponseWriter, r *http.Request) (core.ProposalID, bool) {
	id := core.ProposalID(r.PathValue("id"))
	if err := id.Validate(); err != nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "That proposal doesn't exist (any more).", "Refresh the page and try again.")
		return "", false
	}
	return id, true
}

func (s *Server) handleGetProposal(w http.ResponseWriter, r *http.Request) {
	svc := s.proposals
	if svc == nil {
		notReady(w, "Proposals")
		return
	}
	id, ok := proposalID(w, r)
	if !ok {
		return
	}
	v, err := svc.Get(r.Context(), id)
	if err != nil {
		s.fail(w, r, proposalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, proposalDTO(v))
}

// validActionHash reports whether h is 64 lowercase hex digits (a SHA-256).
func validActionHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// decodeConfirm reads the confirm body. It answers 400 itself when the body
// or the hash is not valid.
func decodeConfirm(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req ConfirmProposalRequest
	if !decodeBody(w, r, &req) {
		return "", false
	}
	if !validActionHash(req.Hash) {
		badRequest(w, "The confirmation must carry the proposal's action hash (64 lowercase hex digits).", "Reload the page and confirm again.")
		return "", false
	}
	return req.Hash, true
}

func (s *Server) handleConfirmProposal(w http.ResponseWriter, r *http.Request) {
	svc := s.proposals
	if svc == nil {
		notReady(w, "Confirming a proposal")
		return
	}
	id, ok := proposalID(w, r)
	if !ok {
		return
	}
	hash, ok := decodeConfirm(w, r)
	if !ok {
		return
	}
	v, err := svc.Confirm(r.Context(), id, hash, decider)
	if err != nil {
		s.fail(w, r, proposalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, proposalDTO(v))
}

func (s *Server) handleRejectProposal(w http.ResponseWriter, r *http.Request) {
	svc := s.proposals
	if svc == nil {
		notReady(w, "Rejecting a proposal")
		return
	}
	id, ok := proposalID(w, r)
	if !ok {
		return
	}
	v, err := svc.Reject(r.Context(), id, decider)
	if err != nil {
		s.fail(w, r, proposalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, proposalDTO(v))
}

func (s *Server) handleRetryProposal(w http.ResponseWriter, r *http.Request) {
	svc := s.proposals
	if svc == nil {
		notReady(w, "Retrying a proposal")
		return
	}
	id, ok := proposalID(w, r)
	if !ok {
		return
	}
	v, err := svc.Retry(r.Context(), id, decider)
	if err != nil {
		s.fail(w, r, proposalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, proposalDTO(v))
}

// ProposalsHub implements proposals.Publisher on the server's WebSocket hub.
// Like HubAdapter it is created before the server and bound after it. Until
// Bind, and after the server is closed, events are dropped. Publish never
// blocks. The message carries IDs and the status only: no summary, no lines.
type ProposalsHub struct{ srv atomic.Pointer[Server] }

var _ proposals.Publisher = (*ProposalsHub)(nil)

// Bind connects the hub to the server. Call it once, before the service starts.
func (h *ProposalsHub) Bind(s *Server) { h.srv.Store(s) }

// Publish tells clients on the "proposals" topic that a proposal changed, and
// refreshes the board (its pending count).
func (h *ProposalsHub) Publish(e proposals.Event) {
	s := h.srv.Load()
	if s == nil {
		return
	}
	s.hub.publishNext(proposalsTopic{}, ProposalChangedMessage{ProposalID: e.ID, TaskID: e.Task, RepoID: e.Repo, Status: e.Status})
	s.hub.publishNext(boardTopic{}, BoardChangedMessage{TaskID: e.Task})
}
