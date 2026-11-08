package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"

	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
)

// The terminal endpoints (DV-04, docs/design/terminal.md). The DTOs are in
// types_terminal.go. This file is Go-only wiring and is excluded from tygo
// (tygo.yaml).
//
// The terminal is never reachable from MCP or any agent path: these two routes
// are the only way in, both behind the UI session, Host/Origin checks and a
// single-use token (invariants 5 and 7).

// TerminalService is *terminal.Service.
type TerminalService interface {
	Issue(ctx context.Context, t ct.Target) (token string, expiresIn time.Duration, err error)
	Redeem(ctx context.Context, token string, size ct.Size) (*terminal.Session, error)
}

func (s *Server) routesTerminal(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/terminal", s.handleTerminalToken)
	mux.HandleFunc("GET /ws/terminal", s.handleTerminalSocket)
}

// terminalFailure writes the plain-language HTTP answer for a terminal error.
func (s *Server) terminalFailure(w http.ResponseWriter, r *http.Request, err error) {
	var re *terminal.RefusedError
	switch {
	case errors.As(err, &re):
		status, code := http.StatusBadRequest, ErrBadRequest
		switch re.Reason {
		case ct.RefusedNotTracked:
			status, code = http.StatusNotFound, ErrNotFound
		case ct.RefusedTooManyPending:
			status, code = http.StatusTooManyRequests, ErrUnavailable
		case ct.RefusedTooManySessions, ct.RefusedCollision:
			status, code = http.StatusConflict, ErrConflict
		case ct.RefusedShuttingDown:
			status, code = http.StatusServiceUnavailable, ErrUnavailable
		}
		writeError(w, status, code, re.Message(), "")
	case errors.Is(err, terminal.ErrNotTracked):
		writeError(w, http.StatusNotFound, ErrNotFound,
			"That folder isn't tracked (any more), so a terminal can't open there.", "Refresh the page and try again.")
	case errors.Is(err, terminal.ErrAuditUnavailable):
		writeError(w, http.StatusServiceUnavailable, ErrUnavailable,
			"The terminal was not opened because Yggdrasil could not record it in the audit log.", "Try again in a moment.")
	default:
		s.fail(w, r, err)
	}
}

func (s *Server) handleTerminalToken(w http.ResponseWriter, r *http.Request) {
	svc := s.phase5().Terminal
	if svc == nil {
		notReady(w, "The terminal")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		badRequest(w, "The request body isn't valid.", "Send the repo or worktree to open a terminal in.")
		return
	}
	target, err := ct.ParseIssue(body)
	if err != nil {
		badRequest(w, "Choose one repo or one worktree for the terminal.", "Send either repoId or worktreeId.")
		return
	}
	token, ttl, err := svc.Issue(r.Context(), target)
	if err != nil {
		s.terminalFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, TerminalTokenResponse{Token: token, ExpiresIn: int(ttl / time.Second)})
}

// handleTerminalSocket redeems the token and bridges the shell to the
// browser. Order matters: Host and Origin first, so a rejected request never
// spends the token; then Redeem; then the upgrade. The token comes from the
// query string and is never logged: the access log records the path only, and
// nothing here prints the request URL.
func (s *Server) handleTerminalSocket(w http.ResponseWriter, r *http.Request) {
	if !s.hostOriginOK(r) {
		reject(w)
		return
	}
	svc := s.phase5().Terminal
	if svc == nil {
		notReady(w, "The terminal")
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		badRequest(w, "The terminal link isn't valid. Open the terminal again.", "")
		return
	}
	if !s.hub.track() {
		writeError(w, http.StatusServiceUnavailable, ErrUnavailable, "Yggdrasil is shutting down.", "Reload the page in a moment.")
		return
	}
	defer s.hub.wg.Done()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)()

	sess, err := svc.Redeem(ctx, token, terminal.DefaultSize)
	if err != nil {
		s.terminalFailure(w, r, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) //nolint:gosec // Host and Origin checked above
	if err != nil {
		sess.Abort() // Accept has written the response
		return
	}
	if err := sess.Run(ctx, newWSSocket(conn)); err != nil {
		s.log.Debug("terminal session ended with an error", "err", err)
	}
}
