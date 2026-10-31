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
	// ErrSessionRequired: no valid UI session cookie (HTTP 401). The page
	// shows how to open Yggdrasil from the link printed in the terminal.
	ErrSessionRequired ErrorCode = "session_required"
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
	Status string `json:"status"`
	// WriteModes: modes other than Ask are on (YGG_WRITE_MODES).
	WriteModes bool   `json:"writeModes"`
	Version    string `json:"version"`
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

// Topic names. A task topic is "task:<id>" and carries agents.Event values in
// WSMessage.Data. TopicTasks carries TaskStatusMessage values for every task.
const (
	TopicTaskPrefix = "task:"
	TopicTasks      = "tasks"
)

// TopicRepos carries ReposChangedMessage: the repo list or its counts changed.
const TopicRepos = "repos"

// TopicRepoPrefix starts "repo:<id>", which carries RepoStatusDTO for that repo.
const TopicRepoPrefix = "repo:"

// TopicBuilds carries BuildChangedMessage for any build.
const TopicBuilds = "builds"

// TopicBuildPrefix starts "build:<id>", which carries BuildChangedMessage and
// BuildLogMessage for that build.
const TopicBuildPrefix = "build:"

// TopicHosts carries HostsChangedMessage: host instances or their MR and
// pipeline data changed.
const TopicHosts = "hosts"

// TaskStatusMessage is the Data of a message on TopicTasks, sent whenever a
// task changes status. It holds no prompt or output, so it is safe to show in
// notifications (docs/AGENTS.md 5.9).
type TaskStatusMessage struct {
	TaskID core.TaskID     `json:"taskId"`
	Title  string          `json:"title"`
	Status core.TaskStatus `json:"status"`
}

// WSMessage is the envelope of every WebSocket message in both directions.
// Seq is per topic and increases by one; a jump means a gap.
type WSMessage struct {
	Type  MessageType     `json:"type"`
	Topic string          `json:"topic,omitempty"`
	Seq   int64           `json:"seq,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// CreateTaskRequest is the body of POST /api/tasks. Mode is "ask", "code",
// "current_branch" or "project". Scope is "repo" or "project"; empty means
// "project" for the project mode and "repo" otherwise. RepoIDs holds exactly
// one repo for a repo scope and none for the project scope. OptIn is the
// person's explicit consent to a mode that edits main checkouts.
type CreateTaskRequest struct {
	ProjectID core.ProjectID `json:"projectId"`
	RepoIDs   []core.RepoID  `json:"repoIds"`
	Scope     string         `json:"scope,omitempty"`
	OptIn     bool           `json:"optIn,omitempty"`
	// Title is the task's name, shown in lists and notifications. It is never
	// made from the prompt, which can hold code or paths (docs/AGENTS.md 5.9).
	// Empty gives "Task <short id>".
	Title  string        `json:"title,omitempty"`
	Prompt string        `json:"prompt"`
	Mode   core.TaskMode `json:"mode"`
	Agent  string        `json:"agent"`
	Model  string        `json:"model,omitempty"`
}

// MessageRequest is the body of POST /api/tasks/{id}/messages: another turn of
// an idle task. The task keeps its project, scope, repos and agent.
type MessageRequest struct {
	Mode   core.TaskMode `json:"mode"`
	Prompt string        `json:"prompt"`
	OptIn  bool          `json:"optIn,omitempty"`
	Model  string        `json:"model,omitempty"`
}

// TaskListResponse is the body of GET /api/tasks (optionally ?status=running).
type TaskListResponse struct {
	Tasks []core.Task `json:"tasks"`
}

// TaskResponse is the body of task endpoints.
type TaskResponse struct {
	Task core.Task `json:"task"`
	// Warning is "edits_main_checkout" or "edits_all_repos" when the task edits
	// main checkouts; empty otherwise.
	Warning string `json:"warning,omitempty"`
}

// TaskEventsResponse is the body of GET /api/tasks/{id}/events: the events
// after `after` (a Seq), used to backfill a transcript.
type TaskEventsResponse struct {
	Events []agents.Event `json:"events"`
}
