package tasks

import (
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// TurnID identifies one turn of a task (a message and the run it starts).
type TurnID string

// Validate reports whether the ID is well formed.
func (id TurnID) Validate() error {
	if id == "" || strings.ContainsAny(string(id), " \t\r\n/\\") {
		return fmt.Errorf("turn id %q: %w", string(id), core.ErrInvalidID)
	}
	return nil
}

// Attempt counts the tries to run one turn, from 1. It never decreases.
type Attempt int

// Limits.
const (
	MaxPromptBytes = 100 * 1024
	MinMaxAgents   = 1
	MaxMaxAgents   = 16
	DefaultMax     = 2
	// RoomHintCeiling is the highest maxAgents RoomHint ever suggests.
	RoomHintCeiling = 8
)

// Usage is what a turn or task has used (AG-15). Cost is micro-USD so sums are
// exact; CostKnown is false when the adapter reports no cost, so "0" is never
// shown for "unknown".
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
	CostMicroUSD     int64
	CostKnown        bool
	DurationMS       int64
}

// Caps are the sub-agent limits applied to a run (AG-22).
type Caps struct {
	Depth      int
	Concurrent int
}

// Subagent is an active sub-agent of a running turn.
type Subagent struct {
	ID    string
	Depth int
}

// Turn is one message of a task and its run. Prompt is stored but never
// audited or notified.
type Turn struct {
	ID       TurnID
	Seq      int
	Mode     core.TaskMode
	Prompt   string
	Attempt  Attempt
	Started  time.Time
	Finished time.Time
	Outcome  string
	Usage    Usage
}

// Task is one task as Step sees it: identity, the mode of the current turn,
// the session to resume, how many turns it has had, and its state.
type Task struct {
	ID        core.TaskID
	Mode      core.TaskMode
	SessionID string
	Turns     int
	// Caps are the sub-agent limits of the current turn (from the Message
	// that started it). Not stored: a live turn is never resumed after a
	// restart, and the shell sets it when it loads a waiting task.
	Caps  Caps
	State State
}
