package agents

import (
	"encoding/json"
	"time"
)

// EventKind is the normalized event type (AG-01).
type EventKind string

const (
	// EventStatus: the run started, finished or changed state; see Status.
	EventStatus EventKind = "status"
	// EventMessage: assistant or user text.
	EventMessage EventKind = "message"
	// EventToolCall: the agent called a tool.
	EventToolCall EventKind = "tool_call"
	// EventToolResult: the result of a tool call (ResultFor holds its ID).
	EventToolResult EventKind = "tool_result"
	// EventFileEdit: the agent changed a file.
	EventFileEdit EventKind = "file_edit"
	// EventCommand: the agent ran a command.
	EventCommand EventKind = "command"
	// EventSubagentStart / EventSubagentEnd bracket an agent-native sub-agent
	// (AG-21). Its own events carry ParentID = the start event's ID.
	EventSubagentStart EventKind = "subagent_start"
	EventSubagentEnd   EventKind = "subagent_end"
	// EventUsage: tokens and cost so far (best effort per adapter).
	EventUsage EventKind = "usage"
	// EventError: something went wrong; Error holds a plain-language message.
	EventError EventKind = "error"
)

// RunStatus is the value of Event.Status.
type RunStatus string

const (
	RunStarted   RunStatus = "started"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// Role of a message.
type Role string

const (
	RoleAssistant Role = "assistant"
	RoleUser      Role = "user"
)

// Event is one normalized event. No core, API or UI code may depend on a
// specific adapter's output format: adapters convert into this type and drop
// anything else (docs/AGENTS.md 5.5). Raw stream lines are never kept,
// because they can carry account data.
type Event struct {
	// Seq is assigned by the runner: 1, 2, 3... per task, without gaps.
	Seq  int64     `json:"seq"`
	Kind EventKind `json:"kind"`
	At   time.Time `json:"at"`
	// ID identifies the event within the task: the tool-call ID for tool
	// calls and sub-agent starts, the message ID for messages.
	ID string `json:"id,omitempty"`
	// ParentID is the ID of the sub-agent start event this event belongs to.
	// Empty for the main agent (AG-01, AG-21).
	ParentID string `json:"parentId,omitempty"`
	// AgentID names the sub-agent that produced the event, when known.
	AgentID string `json:"agentId,omitempty"`

	Status    RunStatus   `json:"status,omitempty"`
	Role      Role        `json:"role,omitempty"`
	Text      string      `json:"text,omitempty"`
	Tool      *ToolCall   `json:"tool,omitempty"`
	ResultFor string      `json:"resultFor,omitempty"`
	IsError   bool        `json:"isError,omitempty"`
	File      *FileEdit   `json:"file,omitempty"`
	Command   *CommandRun `json:"command,omitempty"`
	Usage     *Usage      `json:"usage,omitempty"`
	// Error is a plain-language message for EventError.
	Error string `json:"error,omitempty"`
}

// ToolCall is a tool call: the name and input as the agent sent them.
type ToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// FileEdit is a change to one file, relative to the task's worktree.
type FileEdit struct {
	Path string `json:"path"`
	// Op is "create", "modify" or "delete".
	Op string `json:"op"`
	// Diff is a unified diff, when the adapter can produce one.
	Diff string `json:"diff,omitempty"`
}

// CommandRun is a command the agent ran.
type CommandRun struct {
	Line     string `json:"line"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Output   string `json:"output,omitempty"`
}

// Usage is token usage and cost. Sub-agent usage counts toward the task
// (AG-21): the runner adds up usage events.
type Usage struct {
	InputTokens      int64    `json:"inputTokens,omitempty"`
	OutputTokens     int64    `json:"outputTokens,omitempty"`
	CacheWriteTokens int64    `json:"cacheWriteTokens,omitempty"`
	CacheReadTokens  int64    `json:"cacheReadTokens,omitempty"`
	CostUSD          *float64 `json:"costUsd,omitempty"`
	DurationMS       int64    `json:"durationMs,omitempty"`
	Turns            int      `json:"turns,omitempty"`
	// Cumulative is true when the numbers are totals for the run so far
	// instead of an increment.
	Cumulative bool `json:"cumulative,omitempty"`
}
