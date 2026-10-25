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

// ModelsResponse is the body of GET /api/composer/models?agent=.
type ModelsResponse struct {
	Agent  string     `json:"agent"`
	Models []ModelDTO `json:"models"`
}

// DraftRequest is the body of POST /api/composer/drafts. Kind is
// "commit_message", "mr_title" or "mr_description".
type DraftRequest struct {
	Kind   string      `json:"kind"`
	RepoID core.RepoID `json:"repoId"`
	TaskID core.TaskID `json:"taskId,omitempty"`
	Agent  string      `json:"agent,omitempty"`
}

// DraftResponse is the draft text. The user edits it before anything is
// proposed.
type DraftResponse struct {
	Text string `json:"text"`
}
