package tasks

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// ---- S3: recovered usage belongs to the crashed turn only ----

func TestRecoveredUsageIsBoundedToTheCrashedTurn(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	var crash atomic.Bool
	h.afterCommit = func(_ core.TaskID, cmds []coretasks.Command) bool {
		if !crash.Load() {
			return false
		}
		for _, c := range cmds {
			if _, ok := c.(coretasks.StartRun); ok {
				return true
			}
		}
		return false
	}
	h.start()
	first := h.send(h.msg("ask", "one", 0))
	h.waitStatus(first.Task.ID, core.TaskDone)
	time.Sleep(5 * time.Millisecond) // the second turn starts strictly later
	crash.Store(true)
	req := h.msg("ask", "two", 0)
	req.TaskID = first.Task.ID
	h.send(req)
	waitFor(t, 5*time.Second, func() bool { return isRunning(h.state(first.Task.ID)) }, "the second turn to be running")

	h.restart()
	h.waitStatus(first.Task.ID, core.TaskFailed)
	turns, err := h.store.ListTaskTurns(context.Background(), first.Task.ID)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns = %+v, %v", turns, err)
	}
	if turns[0].Usage.InputTokens != 150 {
		t.Fatalf("first turn usage = %+v", turns[0].Usage)
	}
	if u := turns[1].Usage; u.InputTokens != 0 || u.OutputTokens != 0 || u.CostKnown {
		t.Errorf("the crashed turn was charged the first turn's usage: %+v", u)
	}
}
