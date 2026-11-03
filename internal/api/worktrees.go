package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/git"

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
	// TargetBranch is the main checkout's current branch; "" when unknown.
	TargetBranch string
	// Clean is nil when unknown.
	Clean *bool
}

// worktreeReads is the part of db.Store the worktree service reads.
type worktreeReads interface {
	GetTask(ctx context.Context, id core.TaskID) (core.Task, error)
	ListWorktrees(ctx context.Context, task core.TaskID) ([]core.Worktree, error)
	GetWorktreeBaseCommit(ctx context.Context, id core.WorktreeID) (string, error)
	GetRepo(ctx context.Context, id core.RepoID) (core.Repo, error)
}

// WorktreeGit is what the service reads from git (*git.Git satisfies it).
type WorktreeGit interface {
	CurrentBranch(ctx context.Context, dir string) (string, error)
	Status(ctx context.Context, dir string) (git.Status, error)
}

type dbWorktrees struct {
	store worktreeReads
	git   WorktreeGit
}

// NewWorktreeService serves GET /api/tasks/{id}/worktrees from the database.
// The folder on disk is dropped here, so no caller can leak it.
func NewWorktreeService(store worktreeReads) WorktreeService { return dbWorktrees{store: store} }

// NewWorktreeServiceWithGit also reports the main checkout's branch and
// whether each worktree is clean. What git cannot tell is left out; it never
// fails the list.
func NewWorktreeServiceWithGit(store worktreeReads, g WorktreeGit) WorktreeService {
	return dbWorktrees{store: store, git: g}
}

func (d dbWorktrees) ForTask(ctx context.Context, task core.TaskID) ([]WorktreeView, error) {
	if _, err := d.store.GetTask(ctx, task); err != nil {
		return nil, fmt.Errorf("find task %s: %w", task, err)
	}
	list, err := d.store.ListWorktrees(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("list worktrees of task %s: %w", task, err)
	}
	names := map[core.RepoID]string{}
	targets := map[core.RepoID]string{}
	out := make([]WorktreeView, 0, len(list))
	for _, w := range list {
		name, ok := names[w.RepoID]
		if !ok {
			if r, err := d.store.GetRepo(ctx, w.RepoID); err == nil {
				name = r.Name
				if d.git != nil {
					if b, err := d.git.CurrentBranch(ctx, r.Path); err == nil {
						targets[w.RepoID] = b
					}
				}
			}
			names[w.RepoID] = name
		}
		clean := d.clean(ctx, w)
		base, err := d.store.GetWorktreeBaseCommit(ctx, w.ID)
		if err != nil {
			return nil, fmt.Errorf("read base commit of worktree %s: %w", w.ID, err)
		}
		w.Path = ""
		out = append(out, WorktreeView{Worktree: w, RepoName: name, BaseCommit: base, TargetBranch: targets[w.RepoID], Clean: clean})
	}
	return out, nil
}

// clean reads whether the folder of a worktree that still exists has no
// changes. nil when git is not wired, the folder is gone or git cannot say.
func (d dbWorktrees) clean(ctx context.Context, w core.Worktree) *bool {
	if d.git == nil || w.Path == "" || (w.Status != core.WorktreeActive && w.Status != core.WorktreeMerged) {
		return nil
	}
	st, err := d.git.Status(ctx, w.Path)
	if err != nil {
		return nil
	}
	c := st.Staged+st.Unstaged+st.Untracked+st.Conflicted == 0
	return &c
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
			TargetBranch: v.TargetBranch, Clean: v.Clean,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
