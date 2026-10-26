package tasks

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// ---- M1: the integrity verdict is never lost (SF-07, invariant 9) ----

func violatingAsk(h *harness) {
	h.adapter.during = func(o agents.StartOptions) {
		_ = os.WriteFile(filepath.Join(o.Dir, "README.md"), []byte("changed by the agent\n"), 0o600)
	}
}

func TestIntegrityVerdictSurvivesOneStoreError(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	violatingAsk(h)
	var failed atomic.Int32
	h.beforeCommit = func(_ core.TaskID, in coretasks.Input) error {
		if _, ok := in.(coretasks.IntegrityViolated); ok && failed.Add(1) == 1 {
			return errBoom
		}
		return nil
	}
	h.tune = func(c *Config) { c.FeedRetry = 1 }
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if !h.hasAudit(id, "guard.integrity") {
		t.Errorf("audit = %v", h.audits(id))
	}
}

// Both the verdict and the end of the turn fail to store, then the store is
// back: the recovery of the abandoned task still knows the tree changed.
func TestViolatedAskKeepsItsVerdictAcrossAnAbandon(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	violatingAsk(h)
	var failed atomic.Int32
	h.beforeCommit = func(_ core.TaskID, in coretasks.Input) error {
		switch in.(type) {
		case coretasks.IntegrityViolated, coretasks.RunFinished:
			if failed.Add(1) <= 2*feedTries {
				return errBoom
			}
		}
		return nil
	}
	h.tune = func(c *Config) { c.FeedRetry = 1 }
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if !h.hasAudit(id, "guard.integrity") {
		t.Errorf("audit = %v", h.audits(id))
	}
}

func TestIntegrityVerdictThatCannotBeStoredNeverEndsAnswered(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	violatingAsk(h)
	h.beforeCommit = func(_ core.TaskID, in coretasks.Input) error {
		if _, ok := in.(coretasks.IntegrityViolated); ok {
			return errBoom
		}
		return nil
	}
	h.tune = func(c *Config) { c.FeedRetry = 1 }
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	st := h.waitStatus(id, core.TaskFailed)
	if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailRun}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	for _, s := range h.hub.statusesOf(id) {
		if s == core.TaskDone {
			t.Errorf("statuses %v include done for a violated Ask turn", h.hub.statusesOf(id))
		}
	}
	if !h.hasAudit(id, "guard.integrity") {
		t.Errorf("the integrity audit entries were dropped: %v", h.audits(id))
	}
}
