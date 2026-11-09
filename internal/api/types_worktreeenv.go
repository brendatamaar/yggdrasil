package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of the worktree environment (WE-01 to WE-03, docs/design/worktree-env.md).
//
//	GET /api/repos/{id}/env         RepoEnvDTO
//	PUT /api/repos/{id}/env         RepoEnvDTO -> RepoEnvDTO
//	GET /api/worktrees/{id}/env     WorktreeEnvDTO
//	POST /api/worktrees/{id}/profile/copy   WorktreeEnvDTO
//	POST /api/worktrees/{id}/compose/confirm|up|stop|down   WorktreeEnvDTO

// TopicWorktreeEnv carries WorktreeEnvChangedMessage: a block was allocated,
// a profile copy finished or a compose project changed.
const TopicWorktreeEnv = "worktreeenv"

// RepoEnvDTO is the per-repo environment settings. ProfilePaths are relative
// paths; the server rejects absolute paths, "..", ".git", globs and control
// characters. AgentCopy is off by default: copying env files into a worktree
// makes them readable by the task's agent.
type RepoEnvDTO struct {
	RepoID         core.RepoID `json:"repoId"`
	ProfilePaths   []string    `json:"profilePaths"`
	AgentCopy      bool        `json:"agentCopy"`
	ComposeEnabled bool        `json:"composeEnabled"`
	ComposeFile    string      `json:"composeFile,omitempty"`
}

// ComposeStatus is the closed set of compose project states.
type ComposeStatus string

// The closed set of ComposeStatus values.
const (
	ComposeOff          ComposeStatus = "off"
	ComposeNeedsConfirm ComposeStatus = "needs_confirm"
	ComposeDown         ComposeStatus = "down"
	ComposeStarting     ComposeStatus = "starting"
	ComposeUp           ComposeStatus = "up"
	ComposeStopping     ComposeStatus = "stopping"
	ComposeFailed       ComposeStatus = "failed"
)

// ProfileEntryResult is the outcome of one profile entry in the last copy.
type ProfileEntryResult struct {
	Path   string `json:"path"`
	Result string `json:"result"` // "copied", "skipped" or "failed"
	Reason string `json:"reason,omitempty"`
}

// WorktreeEnvDTO is the environment of one worktree: its port block, the last
// profile copy and the compose project.
type WorktreeEnvDTO struct {
	WorktreeID  core.WorktreeID      `json:"worktreeId"`
	BlockBase   int                  `json:"blockBase,omitempty"`
	BlockSize   int                  `json:"blockSize,omitempty"`
	Profile     []ProfileEntryResult `json:"profile"`
	Compose     ComposeStatus        `json:"compose"`
	ComposeHash string               `json:"composeHash,omitempty"`
	ComposeNote string               `json:"composeNote,omitempty"` // plain language: file lacks YGG_PORT, Docker missing, ...
	UpdatedAt   time.Time            `json:"updatedAt"`
}

// WorktreeEnvChangedMessage is the data of a message on TopicWorktreeEnv.
type WorktreeEnvChangedMessage struct {
	Env WorktreeEnvDTO `json:"env"`
}

// ComposeConfirmRequest is the body of POST /api/worktrees/{id}/compose/confirm.
// Hash is the composeHash the user was shown and is required (400 without it);
// when the file has changed since, nothing is confirmed (409).
type ComposeConfirmRequest struct {
	Hash string `json:"hash"`
}
