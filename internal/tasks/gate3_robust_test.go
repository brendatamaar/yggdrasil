package tasks

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// syncBuf is a log sink that is safe for concurrent use.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func logTo(buf *syncBuf) func(*Config) {
	return func(c *Config) { c.Log = slog.New(slog.NewTextHandler(buf, nil)) }
}

// bogus is a Command the shell does not know: the embedded nil interface gives
// it the core's unexported method.
type bogus struct{ coretasks.Command }

// ---- S6: an unknown command fails loudly ----

func TestUnknownCommandFailsTheTask(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	logs := &syncBuf{}
	h.tune = logTo(logs)
	h.adapter.gate = make(chan struct{})
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return isRunning(h.state(id)) && len(h.adapter.Starts()) == 1 }, "run")
	h.svc.interpret(id, applyResult{Committed: true, Row: h.state(id).Row, Cmds: []coretasks.Command{bogus{}}})
	h.waitStatus(id, core.TaskFailed)
	if !strings.Contains(logs.String(), "unknown task command") {
		t.Errorf("the log does not name the problem: %s", logs.String())
	}
}

// ---- S7: kill, release and publish come before a new prepare or run ----

type spyHub struct {
	*fakeHub
	onStatus func(StatusMessage)
}

func (s spyHub) Status(m StatusMessage) {
	s.onStatus(m)
	s.fakeHub.Status(m)
}

func TestPublishComesBeforeNewWorkStarts(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	_ = h.svc.Close() // nothing runs: the order of the effects is all that is left
	var attemptsAtPublish atomic.Int32
	attemptsAtPublish.Store(-1)
	h.svc.c.Hub = spyHub{fakeHub: h.hub, onStatus: func(StatusMessage) {
		h.svc.mu.Lock()
		attemptsAtPublish.Store(int32(len(h.svc.attempts)))
		h.svc.mu.Unlock()
	}}
	h.svc.interpret("tsk_0000000000000001", applyResult{Committed: true, Cmds: []coretasks.Command{
		coretasks.PrepareRun{Turn: "trn_0000000000000001", Attempt: 1},
		coretasks.Publish{Status: core.TaskDone},
	}})
	if n := attemptsAtPublish.Load(); n != 0 {
		t.Errorf("%d attempt(s) existed when the status was published (-1: never published)", n)
	}
}

// ---- C7: panics are recovered, logged without secrets, and fail the task ----

type panicHub struct {
	*fakeHub
	once atomic.Bool
}

func (p *panicHub) Status(m StatusMessage) {
	if m.Status == core.TaskRunning && p.once.CompareAndSwap(false, true) {
		panic("scripted panic with ghp_abcdefghij0123456789 inside")
	}
	p.fakeHub.Status(m)
}

func TestPanicWhileStartingATaskFailsItAndLogsNoSecret(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	logs := &syncBuf{}
	h.tune = func(c *Config) {
		logTo(logs)(c)
		c.Hub = &panicHub{fakeHub: h.hub}
	}
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	st := h.waitStatus(id, core.TaskFailed)
	if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailInterrupted}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	if strings.Contains(logs.String(), "ghp_abcdefghij0123456789") {
		t.Error("a token reached the log")
	}
	if !strings.Contains(logs.String(), "panic") {
		t.Errorf("the panic was not logged: %s", logs.String())
	}
	// The service still works.
	h.adapter.fixture = "plain"
	next := h.send(h.msg("ask", "again", 0)).Task.ID
	h.waitStatus(next, core.TaskDone)
}

// ---- C9: one unloadable task row does not stop the service from starting ----

func TestUnloadableTaskRowIsQuarantinedAtStart(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	logs := &syncBuf{}
	h.tune = logTo(logs)
	now := time.Now().UTC()
	task := core.Task{
		ID: core.TaskID(db.NewID("tsk")), ProjectID: h.project.ID, Title: "broken", Prompt: "p", Scope: core.ScopeRepo,
		RepoIDs: []core.RepoID{h.repos[0].ID}, Mode: core.ModeAsk, Agent: "mock", Status: core.TaskDraft, CreatedAt: now, UpdatedAt: now,
	}
	ctx := context.Background()
	if err := h.store.Commit(ctx, func(tx *db.Tx) error {
		created, err := tx.CreateTask(ctx, task)
		if err != nil {
			return err
		}
		// Running, with a turn that does not exist: the rows do not load.
		_, err = tx.SaveTask(ctx, task.ID, coretasks.TaskRow{Status: core.TaskRunning, CurrentTurn: "trn_ghost0000000001"}, "", created.Version, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.GetTaskState(ctx, task.ID); err == nil {
		t.Fatal("the broken row loads; the test setup is wrong")
	}
	h.start() // must not fail
	waitFor(t, 5*time.Second, func() bool {
		list, _ := h.store.ListTasks(ctx, h.project.ID)
		for _, tk := range list {
			if tk.ID == task.ID {
				return tk.Status == core.TaskFailed
			}
		}
		return false
	}, "the broken task to be marked failed")
	if !h.hasAudit(task.ID, "task.quarantined") || !strings.Contains(logs.String(), string(task.ID)) {
		t.Errorf("audit = %v, log = %s", h.audits(task.ID), logs.String())
	}
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
}

// ---- C10: audits of the first folders survive a start failure in a later one ----

func TestAskStartFailureKeepsTheAuditsOfEarlierFolders(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.tune = func(c *Config) { c.SnapshotTimeout = 50 * time.Millisecond }
	h.start()
	first := h.send(h.projectMsg("change both"))
	h.waitStatus(first.Task.ID, core.TaskReview)
	var calls atomic.Int32
	h.snap.set(func(ctx context.Context, dir string) (guard.Snapshot, error) {
		if calls.Add(1) == 1 { // the first folder
			return guard.Snapshot{}, git.ErrSnapshotTooLarge // unverifiable: flagged, audited
		}
		<-ctx.Done() // a lost reply: the start is aborted
		return guard.Snapshot{}, ctx.Err()
	})
	req := h.msg("ask", "look", 0)
	req.TaskID = first.Task.ID
	h.send(req)
	waitFor(t, 10*time.Second, func() bool { return !isLive(h.state(first.Task.ID)) && h.state(first.Task.ID).Task.Turns == 2 }, "the Ask turn to end")
	if !h.hasAudit(first.Task.ID, "guard.integrity") {
		t.Errorf("the audit entries of the first folder were dropped: %v", h.audits(first.Task.ID))
	}
}
