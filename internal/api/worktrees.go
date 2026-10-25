package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// This file is owned by the tasks service task (S1; AG-07, WE-04). The DTOs
// are in types_worktrees.go. The file is excluded from tygo (tygo.yaml): it
// holds Go-only wiring.

// WorktreeService is implemented by internal/tasks over internal/worktrees
// (docs/design/tasks.md). Read-only: worktrees are created by the task
// service when a turn starts, never by a request. Errors are classified by
// internal/apperr.
type WorktreeService interface {
	// ForTask lists a task's worktrees, oldest first.
	ForTask(ctx context.Context, task core.TaskID) ([]WorktreeView, error)
}

// WorktreeView is a worktree with the repo name resolved.
type WorktreeView struct {
	Worktree core.Worktree
	RepoName string
	// BaseCommit is the commit the worktree started from.
	BaseCommit string
}

// routesWorktrees registers the worktree endpoints.
func (s *Server) routesWorktrees(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks/{id}/worktrees", s.handleTaskWorktrees)
}

// handleTaskWorktrees is a stub until task S1.
func (s *Server) handleTaskWorktrees(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The list of task worktrees")
}
