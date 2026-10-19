package api

import "time"

// DTOs of the git actions (WS-06 to WS-08): POST /api/git/batches, GET
// /api/git/batches/{id}, POST /api/git/batches/{id}/cancel, POST
// /api/repos/{id}/open, POST /api/repos/{id}/reveal and GET/PUT
// /api/settings/editor.

// StartGitBatchRequest is the body of POST /api/git/batches. Kind is "fetch",
// "pull", "checkout" or "switch". Branch is needed for checkout and switch
// only. Repos are the repos to act on: "fetch all" sends every active repo of
// the project (or all of them from the home page).
type StartGitBatchRequest struct {
	Kind   string   `json:"kind"`
	Repos  []string `json:"repos"`
	Branch string   `json:"branch,omitempty"`
}

// StartGitBatchResponse is the 202 body of POST /api/git/batches.
type StartGitBatchResponse struct {
	BatchID string `json:"batchId"`
}

// GitBatchItemDTO is one repo of a batch. State is "pending", "checking",
// "running", "done" or "skipped". Outcome (done) is "fetched", "pulled",
// "up_to_date", "pull_refused", "checked_out", "checkout_refused" or
// "failed". Reason is the skip reason ("dirty", "no_such_branch",
// "build_running", "untracked", "cancelled") or the refusal or failure kind.
// Message says what to do next.
type GitBatchItemDTO struct {
	RepoID  string     `json:"repoId"`
	State   string     `json:"state"`
	Outcome string     `json:"outcome,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	Message string     `json:"message,omitempty"`
	From    string     `json:"from,omitempty"`
	To      string     `json:"to,omitempty"`
	Branch  string     `json:"branch,omitempty"`
	Created bool       `json:"created,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

// GitBatchDTO is the body of GET /api/git/batches/{id} and of the cancel
// response. A finished batch stays available for about 10 minutes.
type GitBatchDTO struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Branch    string            `json:"branch,omitempty"`
	StartedAt time.Time         `json:"startedAt"`
	Finished  bool              `json:"finished"`
	Items     []GitBatchItemDTO `json:"items"`
}

// GitBatchChangedMessage is the data of a git batch message on the "repos" and
// "repo:<id>" topics (told apart from the other messages by batchId). The
// client refetches GET /api/git/batches/{batchId}.
type GitBatchChangedMessage struct {
	BatchID  string `json:"batchId"`
	Finished bool   `json:"finished"`
}

// OpenRequestBody is the body of POST /api/repos/{id}/open. Editor is
// "vscode", "cursor" or "idea"; empty means the saved or detected editor.
// File is a path inside the repo; empty opens the repo folder.
type OpenRequestBody struct {
	Editor string `json:"editor,omitempty"`
	File   string `json:"file,omitempty"`
}

// RevealRequestBody is the body of POST /api/repos/{id}/reveal.
type RevealRequestBody struct {
	File string `json:"file,omitempty"`
}

// EditorSettingDTO is the body of GET /api/settings/editor. Editor is the
// saved choice ("" when none); Detected lists the supported editors found on
// this computer.
type EditorSettingDTO struct {
	Editor   string   `json:"editor"`
	Detected []string `json:"detected"`
}

// SetEditorRequest is the body of PUT /api/settings/editor. An empty editor
// clears the choice.
type SetEditorRequest struct {
	Editor string `json:"editor"`
}
