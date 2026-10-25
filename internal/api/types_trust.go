package api

import "github.com/brendatamaar/yggdrasil/internal/core"

// DTOs of repo trust (SF-09, docs/design/guard.md): what a repo's own agent
// config would run, and the user's decision to trust it.

// TrustFileDTO is one agent-config file of a repo. WouldRun says in plain
// words what it would start (hooks, MCP servers); Opaque is true when the file
// could not be read as text.
type TrustFileDTO struct {
	Path     string   `json:"path"`
	WouldRun []string `json:"wouldRun"`
	Opaque   bool     `json:"opaque,omitempty"`
}

// TrustDTO is the trust state of a repo. State is "none" (the repo ships no
// agent config), "untrusted" or "trusted". Why is "never_trusted",
// "config_changed", "revoked" or "scan_incomplete" for an untrusted repo.
// Hash is the 64-hex hash of the config shown; trusting must send it back.
type TrustDTO struct {
	RepoID core.RepoID    `json:"repoId"`
	State  string         `json:"state"`
	Why    string         `json:"why,omitempty"`
	Hash   string         `json:"hash,omitempty"`
	Files  []TrustFileDTO `json:"files"`
}

// TrustRequest is the body of POST /api/repos/{id}/trust.
type TrustRequest struct {
	Hash string `json:"hash"`
}
