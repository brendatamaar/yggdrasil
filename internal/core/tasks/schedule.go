package tasks

import "github.com/brendatamaar/yggdrasil/internal/core"

// EntryKind is the coarse state of a task as the board sees it.
type EntryKind string

const (
	KindWaiting  EntryKind = "waiting"
	KindStarting EntryKind = "starting"
	KindRunning  EntryKind = "running"
	KindStopping EntryKind = "stopping"
)

// Entry is one task on the board.
type Entry struct {
	Kind EntryKind
	Mode core.TaskMode
	// EditsMain are the repos whose main checkout the turn edits.
	EditsMain []core.RepoID
	// Queued is the admission order: lower first.
	Queued uint64
}

// Board is the scheduler state: derived from the database on load, not
// stored.
type Board struct {
	Max   int
	Tasks map[core.TaskID]Entry
	// Locks are the granted main-checkout edit locks, by repo.
	Locks map[core.RepoID]core.TaskID
}

// BoardInput is one event for Schedule (sealed).
type BoardInput interface{ isBoardInput() }

// Enqueued: a task has a waiting turn.
type Enqueued struct {
	Task      core.TaskID
	Mode      core.TaskMode
	EditsMain []core.RepoID
	Order     uint64
}

// Removed: a task left the board (it went idle).
type Removed struct{ Task core.TaskID }

// MaxChanged: the user changed maxAgents. Never stops a running task.
type MaxChanged struct{ Max int }

// TaskChanged: a task moved to another kind.
type TaskChanged struct {
	Task core.TaskID
	Kind EntryKind
}

// Wake: something was released; look for tasks to admit.
type Wake struct{}

func (Enqueued) isBoardInput()    {}
func (Removed) isBoardInput()     {}
func (MaxChanged) isBoardInput()  {}
func (TaskChanged) isBoardInput() {}
func (Wake) isBoardInput()        {}

// Schedule is (board, input) -> (board, commands): who may start now
// (maxAgents, one editor per main checkout, work-conserving; the Schedule
// section of docs/design/tasks.md). It emits Admit commands.
//
// STUB (task K1 implements it): returns the board unchanged and no commands.
func Schedule(b Board, in BoardInput) (Board, []Command) {
	_ = in
	return b, nil
}
