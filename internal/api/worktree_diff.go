package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// The worktree diff endpoint (RV-01, PL-03): what the agent changed in one
// repo of a task, against the worktree's base commit. Go-only wiring (excluded
// from tygo); the DTOs are in types_review.go.

// WorktreeDiffer is implemented in cmd/ygg over git.BaseDiffer. It resolves
// the task's worktree for the repo itself, so a client can never name a path:
// only the worktrees of tracked repos of that task are reachable (docs/
// AGENTS.md section 6, invariant 6).
//
// Errors: an apperr NotFound when the task has no worktree for the repo.
type WorktreeDiffer interface {
	DiffWorktree(ctx context.Context, task core.TaskID, repo core.RepoID) (git.BaseDiff, error)
}

// maxDiffTextBytes caps the size of the text one response carries. Files that
// do not fit are listed (with their hashes) but their patch is left out and
// the response is marked truncated.
const maxDiffTextBytes = 4 << 20

func (s *Server) routesWorktreeDiff(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks/{id}/repos/{repo}/diff", s.handleWorktreeDiff)
}

func (s *Server) handleWorktreeDiff(w http.ResponseWriter, r *http.Request) {
	d := s.phase4().WorktreeDiff
	if d == nil {
		notReady(w, "The diff of a task")
		return
	}
	task, ok := pathTaskID(w, r)
	if !ok {
		return
	}
	repo := core.RepoID(r.PathValue("repo"))
	if repo.Validate() != nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "That repo doesn't exist (any more).", "Refresh the page and try again.")
		return
	}
	bd, err := d.DiffWorktree(r.Context(), task, repo)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, worktreeDiffDTO(bd))
}

// worktreeDiffDTO builds the response. Text is the patches of the files in
// path order; a binary file has no patch. Hash is the SHA-256 of that file's
// patch bytes, as git.DiffAgainstBase computes it.
func worktreeDiffDTO(bd git.BaseDiff) WorktreeDiffDTO {
	out := WorktreeDiffDTO{Files: make([]WorktreeDiffFileDTO, 0, len(bd.Files)), Truncated: bd.Truncated}
	var text strings.Builder
	for _, f := range bd.Files {
		fd := WorktreeDiffFileDTO{
			Path: f.Path, Status: f.Status, Binary: f.Binary, Added: f.Added, Deleted: f.Deleted,
			EOLOnly: f.EOLOnly, Hash: f.Hash, PatchTruncated: f.PatchTruncated,
		}
		if f.Binary {
			out.Binary = true
		}
		switch {
		case f.Patch == "":
		case text.Len()+len(f.Patch) > maxDiffTextBytes:
			fd.Omitted = true
			out.Truncated = true
		default:
			text.WriteString(f.Patch)
		}
		out.Files = append(out.Files, fd)
	}
	out.Text = text.String()
	return out
}
