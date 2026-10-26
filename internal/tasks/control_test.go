package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func outcomeOf(t *testing.T, st db.TaskState) coretasks.Outcome {
	t.Helper()
	i, ok := st.Task.State.(coretasks.Idle)
	if !ok {
		t.Fatalf("task is not idle: %#v", st.Task.State)
	}
	return i.Outcome
}

func TestMaxAgentsIsHonoured(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web", "shop-admin"})
	h.adapter.gate = make(chan struct{})
	h.start()
	if err := h.svc.SetMaxAgents(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	var ids []core.TaskID
	for i := 0; i < 3; i++ {
		ids = append(ids, h.send(h.msg("ask", "q", i)).Task.ID)
	}
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "the first run")
	time.Sleep(100 * time.Millisecond)
	if n := len(h.adapter.Starts()); n != 1 {
		t.Fatalf("%d runs started with maxAgents 1", n)
	}
	queued := 0
	for _, id := range ids {
		if h.state(id).Row.Status == core.TaskQueued {
			queued++
		}
	}
	if queued != 2 {
		t.Errorf("%d tasks queued, want 2", queued)
	}
	close(h.adapter.gate)
	for _, id := range ids {
		h.waitStatus(id, core.TaskDone)
	}
	if m := h.adapter.maxLive.Load(); m != 1 {
		t.Errorf("up to %d runs live at once with maxAgents 1", m)
	}
}

func TestRaisingMaxAgentsAdmitsWaitingTasks(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.adapter.gate = make(chan struct{})
	h.start()
	_ = h.svc.SetMaxAgents(context.Background(), "1")
	a := h.send(h.msg("ask", "a", 0)).Task.ID
	b := h.send(h.msg("ask", "b", 1)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "one run")
	if err := h.svc.SetMaxAgents(context.Background(), "2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 2 }, "the second run after raising maxAgents")
	close(h.adapter.gate)
	h.waitStatus(a, core.TaskDone)
	h.waitStatus(b, core.TaskDone)
	if got, _ := h.svc.MaxAgents(context.Background()); got != 2 {
		t.Errorf("maxAgents = %d", got)
	}
	if err := h.svc.SetMaxAgents(context.Background(), "99"); err == nil {
		t.Error("maxAgents 99 accepted")
	}
	if !h.hasSettingAudit() {
		t.Error("no audit entry for the maxAgents change")
	}
}

func (h *harness) hasSettingAudit() bool {
	list, _ := h.store.ListAudit(context.Background(), "settings:maxAgents", 10)
	return len(list) >= 2
}

func TestTwoTasksCannotEditOneMainCheckout(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.adapter.gate = make(chan struct{})
	h.start()
	a := h.send(h.msg("current_branch", "a", 0)).Task.ID
	b := h.send(h.msg("current_branch", "b", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "the first editor")
	time.Sleep(100 * time.Millisecond)
	if n := len(h.adapter.Starts()); n != 1 {
		t.Fatalf("%d editors of one main checkout at once", n)
	}
	if holder := h.lock.held(h.repos[0].ID); holder != "task:"+string(a) {
		t.Errorf("lock holder = %q", holder)
	}
	if h.state(b).Row.Status != core.TaskQueued {
		t.Errorf("second task is %s, want queued", h.state(b).Row.Status)
	}
	close(h.adapter.gate)
	h.waitStatus(a, core.TaskReview)
	h.waitStatus(b, core.TaskReview)
	if m := h.adapter.maxLive.Load(); m != 1 {
		t.Errorf("up to %d editors live at once", m)
	}
	if holder := h.lock.held(h.repos[0].ID); holder != "" {
		t.Errorf("lock still held by %q after both finished", holder)
	}
}

func TestBuildHoldingTheMainCheckoutKeepsATaskWaiting(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.start()
	rel, _, ok := h.lock.TryAcquire([]core.RepoID{h.repos[0].ID}, "build:1")
	if !ok {
		t.Fatal("could not take the lock")
	}
	id := h.send(h.msg("current_branch", "edit", 0)).Task.ID
	time.Sleep(200 * time.Millisecond)
	if n := len(h.adapter.Starts()); n != 0 {
		t.Fatalf("task ran while a build held the checkout (%d starts)", n)
	}
	if st := h.state(id); st.Row.Status != core.TaskQueued {
		t.Fatalf("status = %s, want queued", st.Row.Status)
	}
	rel()
	h.waitStatus(id, core.TaskReview)
}

func TestBlockedTaskNeverLeavesQueuedAndWritesNoAudit(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.start()
	rel, _, ok := h.lock.TryAcquire([]core.RepoID{h.repos[0].ID}, "build:1")
	if !ok {
		t.Fatal("could not take the lock")
	}
	id := h.send(h.msg("current_branch", "edit", 0)).Task.ID
	time.Sleep(60 * time.Millisecond)
	auditsBefore := len(h.audits(id))
	statusesBefore := len(h.hub.statusesOf(id))
	time.Sleep(300 * time.Millisecond) // many LockRetry ticks (20 ms)
	if n := len(h.audits(id)); n != auditsBefore {
		t.Errorf("retry loop wrote %d audit entries", n-auditsBefore)
	}
	statuses := h.hub.statusesOf(id)
	if len(statuses) != statusesBefore {
		t.Errorf("retry loop published %d statuses", len(statuses)-statusesBefore)
	}
	for _, s := range statuses {
		if s == core.TaskRunning {
			t.Errorf("blocked task flapped to running: %v", statuses)
			break
		}
	}
	if st := h.state(id); st.Row.Status != core.TaskQueued {
		t.Errorf("status = %s, want queued", st.Row.Status)
	}
	if card := h.boardCard(t, id); card.BlockedBy != "Waiting: main checkout held by build" {
		t.Errorf("BlockedBy = %q", card.BlockedBy)
	}
	rel()
	h.waitStatus(id, core.TaskReview)
}

func (h *harness) boardCard(t *testing.T, id core.TaskID) BoardCard {
	t.Helper()
	v, err := h.svc.Board(context.Background(), h.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cards {
		if c.Task.ID == id {
			return c
		}
	}
	t.Fatalf("no card for %s", id)
	return BoardCard{}
}

func TestCancelWhileWaiting(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.adapter.gate = make(chan struct{})
	h.start()
	_ = h.svc.SetMaxAgents(context.Background(), "1")
	a := h.send(h.msg("ask", "a", 0)).Task.ID
	b := h.send(h.msg("ask", "b", 1)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "the first run")
	if _, err := h.svc.Cancel(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	st := h.waitStatus(b, core.TaskCancelled)
	if outcomeOf(t, st) != (coretasks.Cancelled{}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	close(h.adapter.gate)
	h.waitStatus(a, core.TaskDone)
	if n := len(h.adapter.Starts()); n != 1 {
		t.Errorf("%d runs started; the cancelled task must never run", n)
	}
	// A cancel of a finished task does nothing.
	if _, err := h.svc.Cancel(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if h.state(a).Row.Status != core.TaskDone {
		t.Error("cancel changed a finished task")
	}
}

func TestCancelWhileRunning(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.gate = make(chan struct{})
	h.start()
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "the run")
	waitFor(t, 5*time.Second, func() bool { return h.state(id).Task.State.Status() == core.TaskRunning && isRunning(h.state(id)) }, "running state")
	if _, err := h.svc.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	st := h.waitStatus(id, core.TaskCancelled)
	if outcomeOf(t, st) != (coretasks.Cancelled{}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	waitFor(t, 5*time.Second, func() bool { return h.adapter.live.Load() == 0 }, "the run to end")
	if h.killer.count(id) == 0 {
		t.Error("processes of the cancelled task were not killed")
	}
	if !h.hasAudit(id, "task.cancel") {
		t.Errorf("audit = %v", h.audits(id))
	}
}

func isRunning(st db.TaskState) bool { _, ok := st.Task.State.(coretasks.Running); return ok }

// TestCancelDuringStarting: the stop phase is not stored, so the service keeps
// the live task in memory; a cancel while worktrees are prepared ends the turn
// as cancelled and never starts the agent (tasks.md, Gate 1 round 3 notes).
func TestCancelDuringStarting(t *testing.T) {
	for _, deaf := range []bool{false, true} {
		name := "preparation honours the cancel"
		if deaf {
			name = "preparation ignores the cancel until it ends"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, []string{"shop-api"})
			h.wts.hold = make(chan struct{})
			h.wts.deaf = deaf
			h.wts.enter = make(chan struct{}, 1)
			h.start()
			id := h.send(h.msg("code", "edit", 0)).Task.ID
			select {
			case <-h.wts.enter:
			case <-time.After(5 * time.Second):
				t.Fatal("worktree preparation never started")
			}
			if _, err := h.svc.Cancel(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if deaf {
				if st := h.state(id); st.Row.Status != core.TaskRunning {
					t.Fatalf("status during the stop = %s", st.Row.Status)
				}
				close(h.wts.hold)
			}
			st := h.waitStatus(id, core.TaskCancelled)
			if outcomeOf(t, st) != (coretasks.Cancelled{}) {
				t.Errorf("outcome = %#v", outcomeOf(t, st))
			}
			if n := len(h.adapter.Starts()); n != 0 {
				t.Errorf("agent started %d time(s) after a cancel during Starting", n)
			}
			// The slot is free again.
			h.adapter.gate = nil
			h.wts.hold = nil
			id2 := h.send(h.msg("ask", "q", 0)).Task.ID
			h.waitStatus(id2, core.TaskDone)
		})
	}
}

// TestEnsureIsSerialisedPerTask: two attempts of one task never run Ensure at
// the same time.
func TestEnsureIsSerialisedPerTask(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	id := h.send(h.msg("code", "edit", 0)).Task.ID
	h.waitStatus(id, core.TaskReview)
	if m := h.wts.maxc.Load(); m != 1 {
		t.Errorf("Ensure ran %d at a time", m)
	}
}

func TestDuplicateDeliveryChangesNothing(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.gate = make(chan struct{})
	h.start()
	res := h.send(h.msg("ask", "q", 0))
	id := res.Task.ID
	waitFor(t, 5*time.Second, func() bool { return isRunning(h.state(id)) && len(h.adapter.Starts()) == 1 }, "the run")
	before := h.state(id).Row.Version

	// The same results again: stale or unhandled, so nothing is stored and no
	// second agent starts.
	h.svc.feed(id, coretasks.Prepared{Turn: res.Turn, Attempt: 1, Dir: "x", Now: time.Now()}, applyOpts{})
	h.svc.feed(id, coretasks.Admitted{Turn: res.Turn, Now: time.Now()}, applyOpts{})
	h.svc.feed(id, coretasks.PrepareFailed{Turn: res.Turn, Attempt: 1, Reason: "dup", Now: time.Now()}, applyOpts{})
	h.svc.feed(id, coretasks.RunFinished{Turn: res.Turn, Attempt: 2, Status: coretasks.RunCompleted, Now: time.Now()}, applyOpts{})
	if got := h.state(id).Row.Version; got != before {
		t.Fatalf("version %d -> %d after duplicates", before, got)
	}
	close(h.adapter.gate)
	st := h.waitStatus(id, core.TaskDone)
	v := st.Row.Version
	h.svc.feed(id, coretasks.RunFinished{Turn: res.Turn, Attempt: 1, Status: coretasks.RunFailed, Now: time.Now()}, applyOpts{})
	h.svc.feed(id, coretasks.CancelRequested{Now: time.Now()}, applyOpts{})
	if got := h.state(id); got.Row.Version != v || got.Row.Status != core.TaskDone {
		t.Fatalf("late duplicates changed a finished task: %+v", got.Row)
	}
	if n := len(h.adapter.Starts()); n != 1 {
		t.Fatalf("%d runs started, want 1", n)
	}
}

// TestCrashBetweenCommitAndInterpret: the process dies after the commit that
// started a run and before the run was started. The next start finds the task
// running and recovers it as interrupted; nothing is restarted.
func TestCrashBetweenCommitAndInterpret(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.afterCommit = func(_ core.TaskID, cmds []coretasks.Command) bool {
		for _, c := range cmds {
			if _, ok := c.(coretasks.StartRun); ok {
				return true
			}
		}
		return false
	}
	h.start()
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return isRunning(h.state(id)) }, "the committed running state")
	time.Sleep(50 * time.Millisecond)
	if len(h.adapter.Starts()) != 0 {
		t.Fatal("the run started despite the crash")
	}

	h.restart()
	st := h.waitStatus(id, core.TaskFailed)
	if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailInterrupted}) {
		t.Errorf("outcome = %#v", outcomeOf(t, st))
	}
	time.Sleep(50 * time.Millisecond)
	if len(h.adapter.Starts()) != 0 {
		t.Error("recovery started a run")
	}
	if !h.hasAudit(id, "task.finish") {
		t.Errorf("audit = %v", h.audits(id))
	}
	// The board is clear: a new task starts at once.
	id2 := h.send(h.msg("ask", "again", 0)).Task.ID
	h.waitStatus(id2, core.TaskDone)
}

// TestWaitingTurnSurvivesARestart: a turn that was queued when the process
// died is queued again, in its old place, and runs.
func TestWaitingTurnSurvivesARestart(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.afterCommit = func(_ core.TaskID, cmds []coretasks.Command) bool {
		for _, c := range cmds {
			if _, ok := c.(coretasks.Enqueue); ok {
				return true // died before the turn reached the board
			}
		}
		return false
	}
	h.start()
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	if h.state(id).Row.Status != core.TaskQueued {
		t.Fatalf("status = %s", h.state(id).Row.Status)
	}
	h.restart()
	h.waitStatus(id, core.TaskDone)
}

// TestRecoveredUsageComesFromStoredEvents: a run that died keeps what it used.
func TestRecoveredUsageComesFromStoredEvents(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.afterCommit = func(_ core.TaskID, cmds []coretasks.Command) bool {
		for _, c := range cmds {
			if _, ok := c.(coretasks.StartRun); ok {
				return true
			}
		}
		return false
	}
	h.start()
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return isRunning(h.state(id)) }, "running")
	cost := 0.01
	for i, e := range []agents.Event{
		{Seq: 1, Kind: agents.EventStatus, Status: agents.RunStarted},
		{Seq: 2, Kind: agents.EventUsage, Usage: &agents.Usage{InputTokens: 100, OutputTokens: 10}},
		{Seq: 3, Kind: agents.EventUsage, Usage: &agents.Usage{InputTokens: 300, OutputTokens: 30, CostUSD: &cost, Cumulative: true}},
	} {
		e.At = time.Now()
		data := mustJSON(t, e)
		if err := h.store.AppendTaskEvent(context.Background(), db.TaskEvent{TaskID: id, Seq: int64(i + 1), Kind: string(e.Kind), At: e.At, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	h.restart()
	h.waitStatus(id, core.TaskFailed)
	turns, _ := h.store.ListTaskTurns(context.Background(), id)
	u := turns[0].Usage
	if u.InputTokens != 300 || u.OutputTokens != 30 || u.CostMicroUSD != 10000 || !u.CostKnown {
		t.Errorf("recovered usage = %+v", u)
	}
}

func TestSumUsageSkipsUndecodableEvents(t *testing.T) {
	rows := []db.TaskEvent{
		{Data: []byte(`{"kind":"status","status":"started"}`)},
		{Data: []byte(`not json`)},
		{Data: []byte(`{"kind":"usage","usage":{"inputTokens":5}}`)},
		{Data: []byte(`{"kind":"usage","usage":{"inputTokens":7}}`)},
		{Data: []byte(`{"kind":"status","status":"started"}`)}, // a later run: earlier ones do not count
		{Data: []byte(`{"kind":"usage","usage":{"inputTokens":1}}`)},
	}
	u, skipped := SumUsage(rows)
	if skipped != 1 || u.InputTokens != 1 || u.CostKnown {
		t.Errorf("u=%+v skipped=%d", u, skipped)
	}
}

// TestLegacyRowsAreInterruptedBeforeLoading: Phase 2 tasks that claim to be
// running are marked interrupted at startup, with an audit entry.
func TestLegacyRowsAreInterruptedBeforeLoading(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	now := time.Now().UTC()
	var ids []core.TaskID
	for _, status := range []core.TaskStatus{core.TaskQueued, core.TaskRunning} {
		task := core.Task{
			ID: core.TaskID(db.NewID("tsk")), ProjectID: h.project.ID, Title: "old", Prompt: "p", Scope: core.ScopeRepo,
			RepoIDs: []core.RepoID{h.repos[0].ID}, Mode: core.ModeAsk, Agent: "mock", Status: status, CreatedAt: now, UpdatedAt: now,
		}
		if err := h.store.Commit(context.Background(), func(tx *db.Tx) error { _, err := tx.CreateTask(context.Background(), task); return err }); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	h.start() // Start fails if a legacy row is left to load
	for _, id := range ids {
		st := h.state(id)
		if outcomeOf(t, st) != (coretasks.Failed{Kind: coretasks.FailInterrupted}) {
			t.Errorf("legacy task outcome = %#v", outcomeOf(t, st))
		}
		if !h.hasAudit(id, "task.interrupted") {
			t.Errorf("audit = %v", h.audits(id))
		}
	}
	// And the new service works.
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBoardColumnsAndHint(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.start()
	ask := h.send(h.msg("ask", "q", 0)).Task.ID
	h.waitStatus(ask, core.TaskDone)
	code := h.send(h.msg("code", "edit", 1)).Task.ID
	h.waitStatus(code, core.TaskReview)
	pending := map[core.TaskID]int{}
	h.svc.c.PendingProposals = func(_ context.Context, id core.TaskID) (int, error) { return pending[id], nil }

	view, err := h.svc.Board(context.Background(), h.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	cols := map[core.TaskID]coretasks.BoardColumn{}
	for _, c := range view.Cards {
		cols[c.Task.ID] = c.Column
	}
	if cols[ask] != coretasks.ColumnDone || cols[code] != coretasks.ColumnReview {
		t.Errorf("columns = %v", cols)
	}
	pending[code] = 2
	view, _ = h.svc.Board(context.Background(), "")
	for _, c := range view.Cards {
		if c.Task.ID == code && (c.Column != coretasks.ColumnNeedsYou || c.PendingProposals != 2) {
			t.Errorf("a finished task with pending proposals sits in %s", c.Column)
		}
	}
	if view.Max != 2 || view.Active != 0 || view.Hint.RaiseTo != 0 {
		t.Errorf("view = %+v", view)
	}
	// Drafts are left out.
	draft := core.Task{ID: core.TaskID(db.NewID("tsk")), ProjectID: h.project.ID, Title: "d", Prompt: "p", Scope: core.ScopeRepo,
		RepoIDs: []core.RepoID{h.repos[0].ID}, Mode: core.ModeAsk, Agent: "mock", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = h.store.Commit(context.Background(), func(tx *db.Tx) error { _, err := tx.CreateTask(context.Background(), draft); return err })
	view, _ = h.svc.Board(context.Background(), h.project.ID)
	for _, c := range view.Cards {
		if c.Task.ID == draft.ID {
			t.Error("a draft is on the board")
		}
	}
	if _, err := h.svc.Dismiss(context.Background(), code); err != nil {
		t.Fatal(err)
	}
	if st := h.state(code); st.Row.Status != core.TaskDone {
		t.Errorf("dismissed task is %s", st.Row.Status)
	}
	if _, err := h.svc.Dismiss(context.Background(), ask); err == nil || !strings.Contains(err.Error(), "ready for review") {
		t.Errorf("dismissing an answered task: %v", err)
	}
}

func TestRoomHintWhenAllSlotsAreBusy(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.gate = make(chan struct{})
	h.start()
	_ = h.svc.SetMaxAgents(context.Background(), "1")
	id := h.send(h.msg("ask", "q", 0)).Task.ID
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "run")
	view, _ := h.svc.Board(context.Background(), "")
	if view.Active != 1 || view.Hint.RaiseTo != 2 {
		t.Errorf("view = %+v, want a hint to raise to 2", view)
	}
	close(h.adapter.gate)
	h.waitStatus(id, core.TaskDone)
}
