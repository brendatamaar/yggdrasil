package tasks

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// ---- S2: a final input that cannot be stored does not leak the slot ----

func TestFailedFinalStoreFreesSlotAndLockThenRecovers(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	var broken atomic.Bool
	broken.Store(true)
	h.beforeCommit = func(_ core.TaskID, in coretasks.Input) error {
		switch in.(type) {
		case coretasks.RunFinished, coretasks.Recovered:
			if broken.Load() {
				return errBoom
			}
		}
		return nil
	}
	h.tune = func(c *Config) { c.FeedRetry = 1 }
	h.start()
	id := h.send(h.msg("current_branch", "edit", 0)).Task.ID
	waitFor(t, 10*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "the run")
	waitFor(t, 10*time.Second, func() bool {
		h.svc.boardMu.Lock()
		defer h.svc.boardMu.Unlock()
		return h.svc.board.Active() == 0 && h.lock.held(h.repos[0].ID) == ""
	}, "the slot and the main checkout lock to be freed")
	if n := h.killer.count(id); n == 0 {
		t.Error("the task's processes were not killed")
	}
	// The stored status is kept for recovery, and the loop recovers it once the
	// store works again.
	broken.Store(false)
	st := h.waitStatus(id, core.TaskFailed)
	if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailInterrupted}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
}
