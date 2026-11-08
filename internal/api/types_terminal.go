package api

import "github.com/brendatamaar/yggdrasil/internal/core"

// DTOs of the terminal dock (DV-04, docs/design/terminal.md).
//
//	POST /api/terminal      TerminalTokenRequest -> TerminalTokenResponse
//	GET  /ws/terminal?token=<token>   WebSocket (not a hub topic)
//
// The client names a repo or a worktree by ID, never a path. The token is
// single use, lives 30 seconds and is never logged or audited.

// TerminalTokenRequest asks for a socket token. Exactly one of RepoID and
// WorktreeID is set.
type TerminalTokenRequest struct {
	RepoID     core.RepoID     `json:"repoId,omitempty"`
	WorktreeID core.WorktreeID `json:"worktreeId,omitempty"`
}

// TerminalTokenResponse carries the raw token, once.
type TerminalTokenResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expiresIn"` // seconds
}

// TerminalResizeFrame is the only JSON text frame a client sends on the
// terminal socket; binary frames are keystrokes and output.
type TerminalResizeFrame struct {
	Type string `json:"type"` // "resize"
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}
