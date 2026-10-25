package tasks

import (
	"fmt"
	"strconv"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// RawMessage is the outside shape of POST /api/tasks and /messages after the
// handler decoded it. ParseMessage turns it into a Message-ready value.
type RawMessage struct {
	Prompt string
	Mode   string
	Scope  string
	Agent  string
	Model  string
	Repos  []string
	Title  string
}

// ParsedMessage is a RawMessage that passed every rule that needs no lookup.
// The shell adds the lookups (repo tracked, agent available) and the turn ID
// and clock.
type ParsedMessage struct {
	Prompt string
	Mode   core.TaskMode
	Scope  core.TaskScope
	Agent  string
	Model  string
	Repos  []core.RepoID
	Title  string
}

// ParseMessage rejects: empty or over-long prompt; unknown mode or scope;
// ask/code with other than one repo; project scope with repos listed; model
// failing the model-name rule. A title with control characters is cleaned.
//
// STUB (task K1 implements it): always an error.
func ParseMessage(r RawMessage) (ParsedMessage, error) {
	_ = r
	return ParsedMessage{}, fmt.Errorf("parse message: %w", ErrNotImplemented)
}

// ParseMax parses the maxAgents setting: a whole number from MinMaxAgents to
// MaxMaxAgents.
func ParseMax(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("maxAgents must be a whole number from %d to %d", MinMaxAgents, MaxMaxAgents)
	}
	if n < MinMaxAgents || n > MaxMaxAgents {
		return 0, fmt.Errorf("maxAgents must be from %d to %d", MinMaxAgents, MaxMaxAgents)
	}
	return n, nil
}

// FormatMax is the inverse of ParseMax.
func FormatMax(n int) string { return strconv.Itoa(n) }

// TaskRow is the part of the tasks row that carries the state.
type TaskRow struct {
	Status      core.TaskStatus
	CurrentTurn TurnID
	// IdleOutcome is the Idle outcome as stored; empty
	// unless Status is idle-like.
	IdleOutcome string
}

// TurnRow is a task_turns row.
type TurnRow struct {
	ID         TurnID
	Seq        int
	Mode       core.TaskMode
	Prompt     string
	Attempt    Attempt
	State      string
	StartedAt  *time.Time
	FinishedAt *time.Time
	Outcome    string
	Usage      Usage
}

// StateFromRows rebuilds a State from the stored rows and rejects rows that
// are inconsistent: unknown status, a live state without its turn or start
// time, an idle task with a turn in flight, a live Attempt below 1.
//
// STUB (task K1 implements it): always an error.
func StateFromRows(t TaskRow, turn *TurnRow) (State, error) {
	_, _ = t, turn
	return nil, fmt.Errorf("read task state: %w", ErrNotImplemented)
}

// RowsOf is the inverse of StateFromRows: StateFromRows(RowsOf(s)) equals s.
//
// STUB (task K1 implements it): fills only the status.
func RowsOf(s State) (TaskRow, *TurnRow) {
	if s == nil {
		return TaskRow{}, nil
	}
	return TaskRow{Status: s.Status()}, nil
}
