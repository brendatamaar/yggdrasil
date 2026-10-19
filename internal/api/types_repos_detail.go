package api

import "time"

// DTOs of the repo page reads (WS-05): GET /api/repos/{id}/changes, /diff,
// /log, /commits/{oid} and /branches.

// FileChangeDTO is one changed path. Index and Worktree are git's one-letter
// codes (M, A, D, R, C, T, U), "?" for untracked, "" for unchanged.
type FileChangeDTO struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath,omitempty"`
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
}

// ChangesResponse is the body of GET /api/repos/{id}/changes.
type ChangesResponse struct {
	Files []FileChangeDTO `json:"files"`
	// Truncated: the list was cut; the counts of the repo status stay exact.
	Truncated bool `json:"truncated"`
}

// DiffResponse is the body of GET /api/repos/{id}/diff.
type DiffResponse struct {
	Text string `json:"text"`
	// Truncated: Text was cut at 2 MiB.
	Truncated bool `json:"truncated"`
	// Binary: at least one file is binary and has no text.
	Binary bool `json:"binary"`
}

// CommitSummaryDTO is one commit of the history.
type CommitSummaryDTO struct {
	OID     string    `json:"oid"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	At      time.Time `json:"at"`
	Parents []string  `json:"parents"`
}

// LogResponse is the body of GET /api/repos/{id}/log.
type LogResponse struct {
	Commits []CommitSummaryDTO `json:"commits"`
}

// CommitFileDTO is one file touched by a commit. Status is git's letter
// (A, M, D, R, C, T).
type CommitFileDTO struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath,omitempty"`
	Status   string `json:"status"`
}

// CommitDetailResponse is the body of GET /api/repos/{id}/commits/{oid}.
type CommitDetailResponse struct {
	Commit CommitSummaryDTO `json:"commit"`
	Body   string           `json:"body"`
	Files  []CommitFileDTO  `json:"files"`
	Diff   DiffResponse     `json:"diff"`
}

// BranchDTO is a local or remote-tracking branch.
type BranchDTO struct {
	Name     string `json:"name"` // "main" or "origin/main"
	Remote   bool   `json:"remote"`
	Current  bool   `json:"current"`
	Upstream string `json:"upstream,omitempty"`
	OID      string `json:"oid"`
}

// BranchesResponse is the body of GET /api/repos/{id}/branches.
type BranchesResponse struct {
	Branches []BranchDTO `json:"branches"`
}
