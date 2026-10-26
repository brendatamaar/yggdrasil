package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

func TestWriteModesOffRefusesEverythingButAsk(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.tune = func(c *Config) { c.WriteModes = false }
	h.start()
	for _, mode := range []string{"code", "current_branch"} {
		_, err := h.svc.Message(context.Background(), h.msg(mode, "edit", 0))
		var rj *RejectedError
		if !errors.As(err, &rj) || rj.Reason != "write_modes_off" {
			t.Fatalf("%s: got %v", mode, err)
		}
	}
	_, err := h.svc.Message(context.Background(), h.projectMsg("edit"))
	var rj *RejectedError
	if !errors.As(err, &rj) || rj.Reason != "write_modes_off" {
		t.Fatalf("project: got %v", err)
	}
	if list, _ := h.store.ListTasks(context.Background(), h.project.ID); len(list) != 0 {
		t.Errorf("a refused message left %d task(s)", len(list))
	}
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
}

// eventPanicHub panics when an event is published after the agent changed a file.
type eventPanicHub struct {
	*fakeHub
	file string
	done atomic.Bool
}

func (p *eventPanicHub) Event(id core.TaskID, seq int64, e agents.Event) {
	if _, err := os.Stat(p.file); err == nil && p.done.CompareAndSwap(false, true) {
		panic("scripted panic in the run")
	}
	p.fakeHub.Event(id, seq, e)
}

func TestPanicInAnAskRunStillChecksTheTree(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.gate = make(chan struct{})
	h.adapter.during = func(o agents.StartOptions) {
		_ = os.WriteFile(filepath.Join(o.Dir, "new.txt"), []byte("x"), 0o600)
	}
	h.tune = func(c *Config) {
		c.Hub = &eventPanicHub{fakeHub: h.hub, file: filepath.Join(h.repos[0].Path, "new.txt")}
	}
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	close(h.adapter.gate)
	st := h.waitStatus(id, core.TaskNeedsYou)
	if outcomeOf(t, st) != (coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
}

func TestBoardCardCarriesTurnUsage(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
	view, err := h.svc.Board(context.Background(), "")
	if err != nil || len(view.Cards) != 1 {
		t.Fatalf("board = %+v, %v", view, err)
	}
	if u := view.Cards[0].Usage; u.InputTokens != 150 || u.OutputTokens == 0 {
		t.Errorf("card usage = %+v", u)
	}
}
