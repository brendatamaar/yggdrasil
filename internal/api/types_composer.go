package api

import "github.com/brendatamaar/yggdrasil/internal/core"

// DTOs of the composer backend (AG-13, AG-20): saved prompts, @ mentions,
// model lists and one-shot drafts.

// SavedPromptDTO is a reusable prompt.
type SavedPromptDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Text string `json:"text"`
}

// SavedPromptsResponse is the body of GET /api/composer/prompts.
type SavedPromptsResponse struct {
	Prompts []SavedPromptDTO `json:"prompts"`
}

// SavePromptRequest is the body of POST /api/composer/prompts.
type SavePromptRequest struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// MentionDTO is a file or folder in a tracked repo that the @ picker offers.
// Path is relative to the repo.
type MentionDTO struct {
	RepoID   core.RepoID `json:"repoId"`
	RepoName string      `json:"repoName"`
	Path     string      `json:"path"`
	// Kind is "file" or "dir".
	Kind string `json:"kind"`
}

// MentionsResponse is the body of GET /api/composer/mentions?project=&q=.
type MentionsResponse struct {
	Mentions  []MentionDTO `json:"mentions"`
	Truncated bool         `json:"truncated"`
}

// ModelDTO is one model an agent offers.
type ModelDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ModelsResponse lists the models of one agent. AllowCustom says the agent
// also accepts a model name that is not in the list.
type ModelsResponse struct {
	Agent       string     `json:"agent"`
	Models      []ModelDTO `json:"models"`
	AllowCustom bool       `json:"allowCustom"`
}

// ModelsListResponse is the body of GET /api/composer/models: one entry per
// registered agent (or only the agent named in ?agent=).
type ModelsListResponse struct {
	Agents []ModelsResponse `json:"agents"`
}

// UpdatePromptRequest is the body of PUT /api/composer/prompts/{id}.
type UpdatePromptRequest struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// DraftRequest is the body of POST /api/composer/drafts. Kind is
// "commit_message" or "mr_description". TaskID, when set, drafts from that
// task's worktree of the repo instead of the main checkout.
type DraftRequest struct {
	Kind      string         `json:"kind"`
	ProjectID core.ProjectID `json:"projectId"`
	RepoID    core.RepoID    `json:"repoId"`
	TaskID    core.TaskID    `json:"taskId,omitempty"`
	Agent     string         `json:"agent,omitempty"`
	Model     string         `json:"model,omitempty"`
}

// DraftResponse is the draft text. The user edits it before anything is
// proposed.
type DraftResponse struct {
	Text string `json:"text"`
	// Title and Description are set for an MR draft: the first line (up to
	// 200 characters) and the rest.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// PushDescription is Description on one line, for a git push option.
	PushDescription string `json:"pushDescription,omitempty"`
}
