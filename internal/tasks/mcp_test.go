package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// fakeMCP records the sessions of runs. A session is live between BeginRun and
// EndRun.
type fakeMCP struct {
	mu       sync.Mutex
	begins   int
	ends     int
	live     map[core.TaskID]bool
	attempt  map[core.TaskID]int
	beginErr error
	token    string
}

func newFakeMCP() *fakeMCP {
	return &fakeMCP{live: map[core.TaskID]bool{}, attempt: map[core.TaskID]int{}, token: "ygg-secret-token-0123456789"}
}

func (f *fakeMCP) BeginRun(_ agents.Adapter, task core.TaskID, attempt int, dir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins++
	if f.beginErr != nil {
		return "", f.beginErr
	}
	f.live[task] = true
	f.attempt[task] = attempt
	return filepath.Join(dir, "mcp.json"), nil
}

func (f *fakeMCP) EndRun(task core.TaskID, attempt int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ends++
	if attempt != 0 && f.attempt[task] != attempt {
		return
	}
	delete(f.live, task)
}

func (f *fakeMCP) isLive(task core.TaskID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[task]
}

func (f *fakeMCP) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.begins, f.ends
}

func withMCP(f *fakeMCP) opt {
	return func(h *harness) {
		prev := h.tune
		h.tune = func(c *Config) {
			if prev != nil {
				prev(c)
			}
			c.MCP, c.RunsDir = f, filepath.Join(h.wtRoot, "runs")
		}
	}
}

func TestMCPSessionLifecycle(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		want  core.TaskStatus
	}{
		{"finished", func(*harness) {}, core.TaskDone},
		{"agent failed", func(h *harness) {
			h.adapter.events = []agents.Event{{Kind: agents.EventMessage, Text: "x"}}
			h.adapter.waitErr = errors.New("boom")
		}, core.TaskFailed},
		{"start failed", func(h *harness) { h.adapter.startErr = errors.New("no binary") }, core.TaskFailed},
		{"panic", func(h *harness) { h.adapter.startPanic = true }, core.TaskFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP()
			h := newHarness(t, []string{"shop-api"}, withMCP(f))
			tc.setup(h)
			var mu sync.Mutex
			var seen bool
			h.adapter.during = func(o agents.StartOptions) {
				mu.Lock()
				defer mu.Unlock()
				seen = o.MCPConfigPath != "" && f.isLive(o.TaskID)
			}
			h.start()
			id := h.send(h.msg("ask", "q", 0)).Task.ID
			h.waitStatus(id, tc.want)
			waitFor(t, 5*time.Second, func() bool { _, e := f.counts(); return e >= 1 }, "session end")
			b, e := f.counts()
			if b != 1 || e < 1 || f.isLive(id) {
				t.Errorf("begins=%d ends=%d live=%v", b, e, f.isLive(id))
			}
			// The run's own config folder goes with its session.
			runs := filepath.Join(h.wtRoot, "runs")
			waitFor(t, 5*time.Second, func() bool {
				es, _ := os.ReadDir(runs)
				return len(es) == 0
			}, "run folder removal")
			mu.Lock()
			defer mu.Unlock()
			if tc.name == "finished" && !seen {
				t.Error("the agent ran without a live session and config path")
			}
		})
	}
}

func TestMCPSessionEndsOnCancel(t *testing.T) {
	f := newFakeMCP()
	h := newHarness(t, []string{"shop-api"}, withMCP(f))
	h.adapter.gate = make(chan struct{})
	h.start()
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return f.isLive(id) }, "session")
	if _, err := h.svc.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(id, core.TaskCancelled)
	waitFor(t, 5*time.Second, func() bool { return !f.isLive(id) }, "session revoked")
}

func TestMCPSessionEndsOnRecovery(t *testing.T) {
	f := newFakeMCP()
	h := newHarness(t, []string{"shop-api"}, withMCP(f))
	h.afterCommit = func(_ core.TaskID, cmds []coretasks.Command) bool {
		for _, c := range cmds {
			if _, ok := c.(coretasks.StartRun); ok {
				return true // the process "died" before the run started
			}
		}
		return false
	}
	h.start()
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return isRunning(h.state(id)) }, "running")
	f.mu.Lock()
	f.live[id], f.attempt[id] = true, 1 // a session that outlived its run
	f.mu.Unlock()
	h.restart()
	h.waitStatus(id, core.TaskFailed)
	if f.isLive(id) {
		t.Error("recovery left the session live")
	}
}

func TestMCPBeginFailureDoesNotStartTheAgent(t *testing.T) {
	f := newFakeMCP()
	f.beginErr = errors.New("write the MCP config: disk full " + f.token)
	h := newHarness(t, []string{"shop-api"}, withMCP(f))
	h.start()
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	h.waitStatus(id, core.TaskFailed)
	if n := len(h.adapter.Starts()); n != 0 {
		t.Errorf("agent started %d times without its MCP config", n)
	}
	if _, e := f.counts(); e < 1 || f.isLive(id) {
		t.Error("a failed begin must leave no session")
	}
	evs, err := h.store.ListTaskEvents(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawPlain bool
	for _, e := range evs {
		if strings.Contains(string(e.Data), f.token) {
			t.Errorf("token in a task event: %s", e.Data)
		}
		if strings.Contains(string(e.Data), "could not be prepared") {
			sawPlain = true
		}
	}
	if !sawPlain {
		t.Error("no plain-language explanation in the transcript")
	}
}

func TestMCPConfigPathReachesTheAgentOncePerRun(t *testing.T) {
	f := newFakeMCP()
	h := newHarness(t, []string{"shop-api"}, withMCP(f))
	h.start()
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
	starts := h.adapter.Starts()
	b, _ := f.counts()
	if len(starts) != 1 || b != 1 {
		t.Fatalf("starts=%d begins=%d", len(starts), b)
	}
	if !strings.HasPrefix(starts[0].MCPConfigPath, filepath.Join(h.wtRoot, "runs")) {
		t.Errorf("config path %q is not under the runs folder", starts[0].MCPConfigPath)
	}
}
