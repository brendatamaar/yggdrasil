package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of review notes (RV-01; docs/design/changesets.md). The handlers are in
// review.go. Changes to notes are announced on TopicChangeSets with Kind
// "notes".

// The values of ReviewNoteDTO.State.
const (
	NoteOpen     = "open"
	NoteSending  = "sending"
	NoteSent     = "sent"
	NoteOutdated = "outdated"
)

// ReviewNoteDTO is one note anchored to a line of a repo's diff. Path is
// repo-relative with forward slashes. Side is "old" or "new". Text is plain
// text: the UI must not render it as markup.
type ReviewNoteDTO struct {
	ID        string      `json:"id"`
	TaskID    core.TaskID `json:"taskId"`
	RepoID    core.RepoID `json:"repoId"`
	Path      string      `json:"path"`
	Line      int         `json:"line"`
	Side      string      `json:"side"`
	Text      string      `json:"text"`
	State     string      `json:"state"`
	CreatedAt time.Time   `json:"createdAt"`
}

// ReviewNotesResponse is the body of GET /api/tasks/{id}/notes.
type ReviewNotesResponse struct {
	Notes []ReviewNoteDTO `json:"notes"`
}

// CreateNoteRequest is the body of POST /api/tasks/{id}/notes. DiffHash is the
// checksum of that file's diff as the client saw it (64 hex).
type CreateNoteRequest struct {
	RepoID   core.RepoID `json:"repoId"`
	Path     string      `json:"path"`
	Line     int         `json:"line"`
	Side     string      `json:"side"`
	Text     string      `json:"text"`
	DiffHash string      `json:"diffHash"`
}

// EditNoteRequest is the body of PUT /api/notes/{id}.
type EditNoteRequest struct {
	Text string `json:"text"`
}

// SendNotesRequest is the body of POST /api/tasks/{id}/notes/send.
type SendNotesRequest struct {
	IDs []string `json:"ids"`
}

// SendNotesResponse is the body of a successful send: the notes now belong to
// TurnID. When the agent is busy the answer is a 409 and the notes stay open.
type SendNotesResponse struct {
	TurnID string          `json:"turnId"`
	Notes  []ReviewNoteDTO `json:"notes"`
}
