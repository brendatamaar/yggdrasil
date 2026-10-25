package tasks

import (
	"cmp"
	"slices"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

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
// section of docs/design/tasks.md). It emits Admit commands, and only on
// Wake: the shell sends a Wake after every enqueue and every release.
//
// Rules on Wake, in queue order (lowest Queued first, ties by task ID):
//   - active (Starting, Running, Stopping) must be below Max;
//   - a turn that edits a main checkout needs every repo in EditsMain free;
//   - a blocked task keeps its place and the scan goes on (work-conserving).
//
// MaxChanged only changes the limit for new starts; it never stops a task.
// A task that goes back to waiting (TaskChanged to KindWaiting, e.g. the lock
// registry said no) or leaves the board gives its locks back.
func Schedule(b Board, in BoardInput) (Board, []Command) {
	switch v := in.(type) {
	case Enqueued:
		if v.Task.Validate() != nil {
			return b, nil
		}
		if e, ok := b.Tasks[v.Task]; ok && e.Kind != KindWaiting {
			return b, nil
		}
		nb := cloneBoard(b)
		nb.Tasks[v.Task] = Entry{Kind: KindWaiting, Mode: v.Mode, EditsMain: editsOf(v.Mode, v.EditsMain), Queued: v.Order}
		return nb, nil
	case Removed:
		if _, ok := b.Tasks[v.Task]; !ok {
			return b, nil
		}
		nb := cloneBoard(b)
		delete(nb.Tasks, v.Task)
		dropLocks(nb, v.Task)
		return nb, nil
	case MaxChanged:
		if v.Max < MinMaxAgents || v.Max > MaxMaxAgents {
			return b, nil
		}
		nb := cloneBoard(b)
		nb.Max = v.Max
		return nb, nil
	case TaskChanged:
		e, ok := b.Tasks[v.Task]
		if !ok || e.Kind == v.Kind || !validKind(v.Kind) {
			return b, nil
		}
		nb := cloneBoard(b)
		e.Kind = v.Kind
		nb.Tasks[v.Task] = e
		if v.Kind == KindWaiting {
			dropLocks(nb, v.Task)
		}
		return nb, nil
	case Wake:
		return admit(b)
	}
	return b, nil
}

func validKind(k EntryKind) bool {
	switch k {
	case KindWaiting, KindStarting, KindRunning, KindStopping:
		return true
	}
	return false
}

// editsOf keeps EditsMain only for a turn that edits a main checkout.
func editsOf(m core.TaskMode, repos []core.RepoID) []core.RepoID {
	if m != core.ModeCurrentBranch {
		return nil
	}
	return append([]core.RepoID(nil), repos...)
}

func cloneBoard(b Board) Board {
	nb := Board{Max: b.Max, Tasks: make(map[core.TaskID]Entry, len(b.Tasks)+1), Locks: make(map[core.RepoID]core.TaskID, len(b.Locks)+1)}
	for k, v := range b.Tasks {
		nb.Tasks[k] = v
	}
	for k, v := range b.Locks {
		nb.Locks[k] = v
	}
	return nb
}

func dropLocks(b Board, task core.TaskID) {
	for repo, owner := range b.Locks {
		if owner == task {
			delete(b.Locks, repo)
		}
	}
}

// Active counts the tasks that have, or are about to have, a process.
func (b Board) Active() int {
	n := 0
	for _, e := range b.Tasks {
		if e.Kind != KindWaiting {
			n++
		}
	}
	return n
}

// lockFree reports whether task may take every main checkout in repos.
func (b Board) lockFree(task core.TaskID, repos []core.RepoID) bool {
	for _, r := range repos {
		if owner, held := b.Locks[r]; held && owner != task {
			return false
		}
	}
	return true
}

func admit(b Board) (Board, []Command) {
	waiting := make([]core.TaskID, 0, len(b.Tasks))
	for id, e := range b.Tasks {
		if e.Kind == KindWaiting {
			waiting = append(waiting, id)
		}
	}
	if len(waiting) == 0 || b.Active() >= b.Max {
		return b, nil
	}
	slices.SortFunc(waiting, func(x, y core.TaskID) int {
		return cmp.Or(cmp.Compare(b.Tasks[x].Queued, b.Tasks[y].Queued), cmp.Compare(x, y))
	})
	nb := cloneBoard(b)
	var cmds []Command
	for _, id := range waiting {
		if nb.Active() >= nb.Max {
			break
		}
		e := nb.Tasks[id]
		if !nb.lockFree(id, e.EditsMain) {
			continue
		}
		e.Kind = KindStarting
		nb.Tasks[id] = e
		for _, r := range e.EditsMain {
			nb.Locks[r] = id
		}
		cmds = append(cmds, Admit{Task: id})
	}
	if len(cmds) == 0 {
		return b, nil
	}
	return nb, cmds
}
