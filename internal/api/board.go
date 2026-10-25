package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// This file is owned by the tasks service task (S1; AG-11, AG-12, AG-15). The
// DTOs are in types_board.go. The file is excluded from tygo (tygo.yaml): it
// holds Go-only wiring.

// BoardService is implemented by internal/tasks (docs/design/tasks.md).
// Errors are classified by internal/apperr.
type BoardService interface {
	// Board returns every task card, the maxAgents setting, the number of
	// active tasks and the room hint.
	Board(ctx context.Context) (BoardView, error)
	// SetMaxAgents stores the setting (1 to 16). It never stops a running task.
	SetMaxAgents(ctx context.Context, n int) error
}

// BoardView is what the board shows.
type BoardView struct {
	MaxAgents int
	Active    int
	Hint      coretasks.Hint
	Cards     []BoardCard
}

// BoardCard is one task on the board.
type BoardCard struct {
	Task             core.Task
	Column           coretasks.BoardColumn
	RepoNames        []string
	PendingProposals int
	// BlockedBy is the plain-language reason a queued task waits, or "".
	BlockedBy string
	Usage     coretasks.Usage
}

// routesBoard registers the board endpoints.
func (s *Server) routesBoard(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/board", s.handleGetBoard)
	mux.HandleFunc("PUT /api/board/max-agents", s.handleSetMaxAgents)
}

// The handlers are stubs until task S1.

func (s *Server) handleGetBoard(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The agent board")
}

func (s *Server) handleSetMaxAgents(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "Changing the number of agents")
}
