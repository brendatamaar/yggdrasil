package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of task worktrees (AG-07, WE-04): GET /api/tasks/{id}/worktrees.

// WorktreeDTO is one worktree of a task. Path is the folder on disk, shown so
// the user can open it; it is never sent to agents or MCP.
type WorktreeDTO struct {
	ID         core.WorktreeID     `json:"id"`
	RepoID     core.RepoID         `json:"repoId"`
	RepoName   string              `json:"repoName"`
	TaskID     core.TaskID         `json:"taskId"`
	Name       string              `json:"name"`
	Branch     string              `json:"branch"`
	BaseCommit string              `json:"baseCommit"`
	Path       string              `json:"path"`
	Status     core.WorktreeStatus `json:"status"`
	CreatedAt  time.Time           `json:"createdAt"`
}

// WorktreesResponse is the body of GET /api/tasks/{id}/worktrees.
type WorktreesResponse struct {
	Worktrees []WorktreeDTO `json:"worktrees"`
}
