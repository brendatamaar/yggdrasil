package tasks

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var boardTasks = []core.TaskID{"a", "b", "c", "d", "e", "f"}

// genBoardInput is biased to legal use: tasks go active only through Wake.
func genBoardInput(r *rand.Rand, b Board, order *uint64) BoardInput {
	id := boardTasks[r.Intn(len(boardTasks))]
	switch r.Intn(9) {
	case 0, 1, 2:
		*order++
		mode := allModes[r.Intn(4)]
		var edits []core.RepoID
		if mode == core.ModeCurrentBranch {
			edits = []core.RepoID{core.RepoID(fmt.Sprintf("r%d", r.Intn(2)))}
			if r.Intn(4) == 0 {
				edits = append(edits, "r2")
			}
		}
		return Enqueued{Task: id, Mode: mode, EditsMain: edits, Order: *order}
	case 3, 4, 5:
		return Wake{}
	case 6:
		return MaxChanged{Max: r.Intn(6)} // 0 is invalid on purpose
	case 7:
		return Removed{Task: id}
	default:
		kinds := []EntryKind{KindStarting, KindRunning, KindStopping, KindWaiting}
		return TaskChanged{Task: id, Kind: kinds[r.Intn(4)]}
	}
}

func driveBoard(t *testing.T, check func(prev Board, in BoardInput, next Board, cmds []Command) error) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		b := Board{Max: 1 + r.Intn(3), Tasks: map[core.TaskID]Entry{}, Locks: map[core.RepoID]core.TaskID{}}
		var order uint64
		for i := 0; i < 80; i++ {
			in := genBoardInput(r, b, &order)
			// Tasks only move back to waiting or on while active, as in
			// production: a waiting task becomes active through Wake alone.
			if tc, ok := in.(TaskChanged); ok {
				if e, on := b.Tasks[tc.Task]; !on || e.Kind == KindWaiting {
					in = Wake{}
				}
			}
			next, cmds := Schedule(b, in)
			if err := check(b, in, next, cmds); err != nil {
				t.Fatalf("step %d, input %#v\nprev %#v\nnext %#v\ncmds %#v\n%v", i, in, b, next, cmds, err)
			}
			b = next
		}
	})
}

func activeOf(b Board) int {
	n := 0
	for _, e := range b.Tasks {
		if e.Kind != KindWaiting {
			n++
		}
	}
	return n
}

// K1: no step makes the active count exceed Max by admitting; only lowering
// Max can leave it above, and then nothing new starts.
func TestPropActiveNeverExceedsMax(t *testing.T) {
	driveBoard(t, func(prev Board, in BoardInput, next Board, cmds []Command) error {
		if len(cmds) > 0 && activeOf(next) > next.Max {
			return fmt.Errorf("admitted past Max: active %d, Max %d", activeOf(next), next.Max)
		}
		if activeOf(next) > max(next.Max, activeOf(prev)) {
			return fmt.Errorf("active grew past Max")
		}
		if mc, ok := in.(MaxChanged); ok {
			if mc.Max < prev.Max && activeOf(next) != activeOf(prev) {
				return fmt.Errorf("lowering Max stopped or started a task")
			}
			for id, e := range prev.Tasks {
				if e.Kind != KindWaiting && next.Tasks[id].Kind != e.Kind {
					return fmt.Errorf("MaxChanged moved running task %s", id)
				}
			}
		}
		return nil
	})
}

// K2: at most one active task edits a given main checkout, and the lock table
// holds exactly the locks of the active editors.
func TestPropOneEditorPerMainCheckout(t *testing.T) {
	driveBoard(t, func(prev Board, in BoardInput, next Board, cmds []Command) error {
		holders := map[core.RepoID][]core.TaskID{}
		for id, e := range next.Tasks {
			if e.Kind == KindWaiting {
				continue
			}
			for _, r := range e.EditsMain {
				holders[r] = append(holders[r], id)
			}
		}
		for r, ids := range holders {
			if len(ids) > 1 {
				return fmt.Errorf("repo %s edited by %v", r, ids)
			}
			if next.Locks[r] != ids[0] {
				return fmt.Errorf("repo %s edited by %s but locked by %q", r, ids[0], next.Locks[r])
			}
		}
		for r, owner := range next.Locks {
			if !slices.Contains(holders[r], owner) {
				return fmt.Errorf("lock on %s held by %s, which is not an active editor", r, owner)
			}
		}
		return nil
	})
}

// K11: after a Wake no waiting task could still be admitted (work-conserving),
// checked with a separate oracle, and admissions go in queue order among the
// tasks that could run.
func TestPropWorkConserving(t *testing.T) {
	driveBoard(t, func(prev Board, in BoardInput, next Board, cmds []Command) error {
		if _, ok := in.(Wake); !ok {
			if len(cmds) != 0 {
				return fmt.Errorf("only Wake admits")
			}
			return nil
		}
		active := activeOf(next)
		held := map[core.RepoID]bool{}
		for id, e := range next.Tasks {
			if e.Kind != KindWaiting {
				for _, r := range e.EditsMain {
					held[r] = true
				}
			}
			_ = id
		}
		for id, e := range next.Tasks {
			if e.Kind != KindWaiting || active >= next.Max {
				continue
			}
			free := true
			for _, r := range e.EditsMain {
				if held[r] {
					free = false
				}
			}
			if free {
				return fmt.Errorf("task %s could run but waits (active %d, Max %d)", id, active, next.Max)
			}
		}
		// Order: a task admitted now is not behind a waiting task that was
		// admissible when it was chosen and has an earlier place.
		for _, c := range cmds {
			a := c.(Admit)
			for id, e := range prev.Tasks {
				if e.Kind != KindWaiting || id == a.Task {
					continue
				}
				if next.Tasks[id].Kind == KindWaiting && e.Queued < prev.Tasks[a.Task].Queued && len(e.EditsMain) == 0 {
					return fmt.Errorf("task %s (place %d) skipped for %s", id, e.Queued, a.Task)
				}
			}
		}
		return nil
	})
}
