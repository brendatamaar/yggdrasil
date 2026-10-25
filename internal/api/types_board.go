package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of the agent board and task usage (AG-11, AG-12, AG-15).

// TopicBoard carries BoardChangedMessage: a task moved between columns or the
// maxAgents setting changed. The client refetches the board.
const TopicBoard = "board"

// BoardChangedMessage is the data of a message on TopicBoard. It holds no
// prompt or output (docs/AGENTS.md 5.9).
type BoardChangedMessage struct {
	TaskID core.TaskID `json:"taskId,omitempty"`
	Column string      `json:"column,omitempty"`
}

// UsageDTO is tokens and cost. Cost is in micro-USD (1,000,000 = one dollar);
// CostKnown is false when the agent reports no cost, so the UI shows "unknown"
// instead of zero.
type UsageDTO struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CostMicroUSD     int64 `json:"costMicroUsd"`
	CostKnown        bool  `json:"costKnown"`
	DurationMS       int64 `json:"durationMs"`
}

// BoardCardDTO is one task on the board. Column is "working", "needs_you",
// "review" or "done". BlockedBy says in plain words why a queued task has not
// started.
type BoardCardDTO struct {
	TaskID           core.TaskID     `json:"taskId"`
	Title            string          `json:"title"`
	Mode             core.TaskMode   `json:"mode"`
	Agent            string          `json:"agent"`
	Status           core.TaskStatus `json:"status"`
	Column           string          `json:"column"`
	RepoNames        []string        `json:"repoNames"`
	PendingProposals int             `json:"pendingProposals"`
	BlockedBy        string          `json:"blockedBy,omitempty"`
	Usage            UsageDTO        `json:"usage"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

// RoomHintDTO suggests raising maxAgents; RaiseTo is 0 when there is no
// suggestion.
type RoomHintDTO struct {
	RaiseTo int `json:"raiseTo"`
}

// BoardResponse is the body of GET /api/board.
type BoardResponse struct {
	MaxAgents int            `json:"maxAgents"`
	Active    int            `json:"active"`
	Hint      RoomHintDTO    `json:"hint"`
	Cards     []BoardCardDTO `json:"cards"`
}

// SetMaxAgentsRequest is the body of PUT /api/board/max-agents (1 to 16).
type SetMaxAgentsRequest struct {
	MaxAgents int `json:"maxAgents"`
}
