package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// This file is Go-only wiring and is excluded from tygo (tygo.yaml).

// TaskService is what the task, board and trust endpoints need. internal/tasks
// implements it (*tasks.Service); the handlers know nothing else of that
// package.
//
// The first six methods exist on *tasks.Service already. The trust methods are
// implemented in internal/tasks/trust.go with exactly these signatures:
//
//	func (s *Service) TrustStatus(ctx context.Context, repo core.RepoID) (guard.Trust, error)
//	func (s *Service) Trust(ctx context.Context, repo core.RepoID, hash string) (guard.Trust, error)
//	func (s *Service) Revoke(ctx context.Context, repo core.RepoID) (guard.Trust, error)
//
// TrustStatus scans the repo and returns its state. Trust records trust in the
// hash the user was shown: when that hash is no longer the repo's current
// config it returns the unchanged (not Trusted) state and a nil error, and the
// handler answers 409. Revoke withdraws trust. A repo that does not exist is
// db.ErrNotFound.
//
// Errors the handlers translate: *coretasks.UserError (400), *tasks.RejectedError
// (409), db.ErrNotFound (404), db.ErrConflict (409); anything else is a generic
// 500.
type TaskService interface {
	// Message creates a task (req.TaskID empty) or adds a turn to an idle one.
	Message(ctx context.Context, req tasks.MessageRequest) (tasks.MessageResult, error)
	Cancel(ctx context.Context, id core.TaskID) (core.Task, error)
	Dismiss(ctx context.Context, id core.TaskID) (core.Task, error)
	// Board returns the cards of one project, or of every project when empty.
	Board(ctx context.Context, project core.ProjectID) (tasks.BoardView, error)
	MaxAgents(ctx context.Context) (int, error)
	// SetMaxAgents takes the decimal text of the new value (1 to 16).
	SetMaxAgents(ctx context.Context, value string) error

	TrustStatus(ctx context.Context, repo core.RepoID) (guard.Trust, error)
	Trust(ctx context.Context, repo core.RepoID, hash string) (guard.Trust, error)
	Revoke(ctx context.Context, repo core.RepoID) (guard.Trust, error)
}

// serviceErr answers an error of the task service: what is the person's to fix
// is shown in the service's own plain text, a missing item is a 404, a changed
// one a 409, and everything else a generic 500 (details only in the log).
func (s *Server) serviceErr(w http.ResponseWriter, r *http.Request, err error) {
	var ue *coretasks.UserError
	var re *tasks.RejectedError
	switch {
	case errors.As(err, &ue):
		badRequest(w, ue.Message, "")
	case errors.As(err, &re):
		writeError(w, http.StatusConflict, ErrConflict, re.Message, "")
	default:
		s.fail(w, r, taskErr(err))
	}
}
