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

// handleTaskWorktrees lists a task's worktrees. The folder on disk is not sent:
// the person opens a worktree through its own action, and agents and MCP never
// see paths.
func (s *Server) handleTaskWorktrees(w http.ResponseWriter, r *http.Request) {
	if s.worktrees == nil {
		notReady(w, "The list of task worktrees")
		return
	}
	id := core.TaskID(r.PathValue("id"))
	if id.Validate() != nil {
		badRequest(w, "That task ID isn't valid.", "")
		return
	}
	views, err := s.worktrees.ForTask(r.Context(), id)
	if err != nil {
		s.fail(w, r, taskErr(err))
		return
	}
	out := WorktreesResponse{Worktrees: []WorktreeDTO{}}
	for _, v := range views {
		wt := v.Worktree
		out.Worktrees = append(out.Worktrees, WorktreeDTO{
			ID: wt.ID, RepoID: wt.RepoID, RepoName: v.RepoName, TaskID: wt.TaskID, Name: wt.Name,
			Branch: wt.Branch, BaseCommit: v.BaseCommit, Status: wt.Status, CreatedAt: wt.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
