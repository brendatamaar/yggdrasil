package agentstream

import (
	"encoding/json"
	"strings"
)

// Kind is the normalized event type.
type Kind string

// Event kinds. Draft for the Phase 1 Event in internal/agents.
const (
	KindInit          Kind = "init"
	KindText          Kind = "text"
	KindToolCall      Kind = "tool_call"
	KindToolResult    Kind = "tool_result"
	KindSubagentStart Kind = "subagent_start"
	// KindTaskStarted: system/task_started, a sub-agent (or background task)
	// began. ID is its tool_use id, AgentID its task_id.
	KindTaskStarted Kind = "task_started"
	// KindPermissionDenied: system/permission_denied, the CLI's own
	// permission check refused a call. ResultFor is its tool_use id.
	KindPermissionDenied Kind = "permission_denied"
	KindUsage            Kind = "usage"
	KindResult           Kind = "result"
	KindError            Kind = "error"
	KindUnknown          Kind = "unknown"
)

// Event is one normalized event. One stream line can produce several events
// (one per content block, plus usage); they share Line and Raw.
type Event struct {
	// Line is the 1-based line number in the stream.
	Line int  `json:"line"`
	Kind Kind `json:"kind"`
	// ID: the tool_use id for tool calls and sub-agent starts, the message id
	// for text and usage, the session id for init and result.
	ID string `json:"id,omitempty"`
	// ParentID is the tool_use id of the sub-agent call this event belongs to
	// (Claude Code parent_tool_use_id). Empty for the main agent.
	ParentID string `json:"parentId,omitempty"`
	// AgentID is the sub-agent that produced the event (agent_id; the same
	// id hooks get as agent_id). Empty for the main agent.
	AgentID string `json:"agentId,omitempty"`
	// Depth is the sub-agent layer of a task_started event (spawn_depth).
	Depth     int    `json:"depth,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Model     string `json:"model,omitempty"`
	// Role is "assistant" or "user" for text events.
	Role string `json:"role,omitempty"`
	Text string `json:"text,omitempty"`
	Tool *Tool  `json:"tool,omitempty"`
	// ResultFor is the tool_use id a tool result answers.
	ResultFor string `json:"resultFor,omitempty"`
	IsError   bool   `json:"isError,omitempty"`
	Usage     *Usage `json:"usage,omitempty"`
	// Err describes a parse problem (KindError) or an unhandled block.
	Err string `json:"error,omitempty"`
	// Raw is the original stream line, kept for every kind.
	Raw string `json:"raw,omitempty"`
}

// Tool is a tool call: name and input as sent by the agent.
type Tool struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Usage is token usage and, on the final result, cost.
type Usage struct {
	InputTokens              int64    `json:"inputTokens,omitempty"`
	OutputTokens             int64    `json:"outputTokens,omitempty"`
	CacheCreationInputTokens int64    `json:"cacheCreationInputTokens,omitempty"`
	CacheReadInputTokens     int64    `json:"cacheReadInputTokens,omitempty"`
	CostUSD                  *float64 `json:"costUsd,omitempty"`
	DurationMS               int64    `json:"durationMs,omitempty"`
	NumTurns                 int      `json:"numTurns,omitempty"`
}

// subagentTools are the tool names that spawn a sub-agent. The tool is
// "Agent" (renamed from "Task" in CLI 2.1.63; "Task" stays an alias, and the
// init message's tools list still says "Task"), per /sub-agents and the Agent
// SDK reference.
var subagentTools = []string{"Agent", "Task"}

// IsSubagentTool reports whether a tool name spawns a sub-agent.
func IsSubagentTool(name string) bool {
	for _, t := range subagentTools {
		if strings.EqualFold(name, t) {
			return true
		}
	}
	return false
}

// SubagentInput is the part of a sub-agent tool's input the harness reads
// (Agent SDK AgentInput). Isolation is "worktree" or "remote"; the hook
// blocks both.
type SubagentInput struct {
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
	Isolation    string `json:"isolation"`
}

// ParseSubagentInput reads a sub-agent tool input; unknown fields are ignored.
func ParseSubagentInput(input json.RawMessage) SubagentInput {
	var in SubagentInput
	if len(input) > 0 {
		_ = json.Unmarshal(input, &in)
	}
	return in
}

// WantsWorktree reports whether a sub-agent input asks for agent-native
// worktree isolation (AG-22: blocked inside Yggdrasil tasks).
func (s SubagentInput) WantsWorktree() bool {
	return strings.EqualFold(strings.TrimSpace(s.Isolation), "worktree")
}
