package api

// DTOs of landing: the merge_local and remove_worktree proposals (RV-05,
// RV-06; docs/design/landing.md). The handlers are in landing.go. Both routes
// only CREATE a proposal; nothing changes until the user confirms it through
// the proposal routes.

// MergeLocalRequest is the body of POST /api/worktrees/{id}/merge-local. The
// repo, branch, target branch and branch tip come from the worktree and the
// main checkout, never from the client. Title is the merge commit's subject; it
// defaults to the task title.
type MergeLocalRequest struct {
	Title string `json:"title,omitempty"`
}
