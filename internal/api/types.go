package api

import (
	"encoding/json"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Request and response types shared with the frontend. `task generate` turns
// them into web/src/api/types.gen.ts: never hand-edit that file, and never
// change a type without running it (docs/AGENTS.md 5.8).

// ErrorCode is a stable machine-readable error class.
type ErrorCode string

const (
	ErrBadRequest   ErrorCode = "bad_request"
	ErrNotFound     ErrorCode = "not_found"
	ErrForbidden    ErrorCode = "forbidden"
	ErrConflict     ErrorCode = "conflict"
	ErrUnavailable  ErrorCode = "unavailable"
	ErrInternal     ErrorCode = "internal"
	ErrHostRejected ErrorCode = "host_rejected"
)

// ErrorBody is the JSON body of every error response (SF-01). Message says
// what went wrong in plain language; Hint says what to do next. Neither
// carries internal error text, paths or tokens.
type ErrorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Hint    string    `json:"hint,omitempty"`
}

// HealthResponse is the body of GET /api/health.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// MessageType is the type of a WebSocket message.
type MessageType string

const (
	// MsgSubscribe / MsgUnsubscribe are sent by the client.
	MsgSubscribe   MessageType = "subscribe"
	MsgUnsubscribe MessageType = "unsubscribe"
	// MsgEvent carries data for a topic; MsgGap tells the client it missed
	// messages and must refetch (see Seq).
	MsgEvent MessageType = "event"
	MsgGap   MessageType = "gap"
	// MsgError reports a problem with the client's last message.
	MsgError MessageType = "error"
)

// Topic names. Task topics are "task:<id>".
const TopicTaskPrefix = "task:"

// WSMessage is the envelope of every WebSocket message in both directions.
// Seq is per topic and increases by one; a jump means a gap.
type WSMessage struct {
	Type  MessageType     `json:"type"`
	Topic string          `json:"topic,omitempty"`
	Seq   int64           `json:"seq,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// CreateTaskRequest is the body of POST /api/tasks.
type CreateTaskRequest struct {
	ProjectID core.ProjectID `json:"projectId"`
	RepoIDs   []core.RepoID  `json:"repoIds"`
	Prompt    string         `json:"prompt"`
	Mode      core.TaskMode  `json:"mode"`
	Agent     string         `json:"agent"`
	Model     string         `json:"model,omitempty"`
}

// TaskResponse is the body of task endpoints.
type TaskResponse struct {
	Task core.Task `json:"task"`
}

// TaskEventsResponse is the body of GET /api/tasks/{id}/events: the events
// after `after` (a Seq), used to backfill a transcript.
type TaskEventsResponse struct {
	Events []agents.Event `json:"events"`
}
