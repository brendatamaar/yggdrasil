package api

import "github.com/brendatamaar/yggdrasil/internal/core"

// DevServerActionRequest is the optional body of POST /api/devservers/{id}/start,
// stop and restart. A server that was never started has no entry to find, so
// the first start names the target; the server checks that the ID in the
// address is the ID of this repo, worktree and command. Clients send IDs and
// the command's name, never a folder.
type DevServerActionRequest struct {
	RepoID     core.RepoID     `json:"repoId"`
	WorktreeID core.WorktreeID `json:"worktreeId,omitempty"`
	Command    string          `json:"command"`
}
