package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

func TestAskTaskRunsToDone(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	res := h.send(h.msg("ask", "What does this do?", 0))
	st := h.waitStatus(res.Task.ID, core.TaskDone)

	idle, ok := st.Task.State.(coretasks.Idle)
	if !ok || idle.Outcome != (coretasks.Answered{}) || idle.Turn != res.Turn {
		t.Fatalf("state = %#v", st.Task.State)
	}
	if st.Task.SessionID == "" || st.Task.Turns != 1 {
		t.Errorf("session %q turns %d", st.Task.SessionID, st.Task.Turns)
	}
	turns, err := h.store.ListTaskTurns(context.Background(), res.Task.ID)
	if err != nil || len(turns) != 1 {
		t.Fatalf("turns = %v %v", turns, err)
	}
	tr := turns[0]
	if tr.State != coretasks.TurnFinished || tr.Outcome != "answered" || tr.Prompt != "What does this do?" || tr.Attempt != 1 {
		t.Errorf("turn row = %+v", tr)
	}
	// 120+30 input tokens, 40+15 output, 3000 micro-USD, known.
	if u := tr.Usage; u.InputTokens != 150 || u.OutputTokens != 55 || u.CostMicroUSD != 3000 || !u.CostKnown {
		t.Errorf("usage = %+v", u)
	}
	if h.killer.count(res.Task.ID) == 0 {
		t.Error("the task's processes were not killed when it finished")
	}
	for _, want := range []string{"task.turn", "task.start", "task.run", "task.finish"} {
		if !h.hasAudit(res.Task.ID, want) {
			t.Errorf("no audit entry %s in %v", want, h.audits(res.Task.ID))
		}
	}
	got := h.hub.statusesOf(res.Task.ID)
	if len(got) < 3 || got[0] != core.TaskQueued || got[len(got)-1] != core.TaskDone {
		t.Errorf("published statuses = %v", got)
	}
	ev, _ := h.store.ListTaskEvents(context.Background(), res.Task.ID, 0)
	if len(ev) < 5 {
		t.Fatalf("events = %d", len(ev))
	}
	for i, e := range ev {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
	// The run got the clean admission and a policy with no write root.
	o := h.adapter.Starts()[0]
	if !o.Admitted || o.Policy.Mode != core.ModeAsk || len(o.Policy.WriteRoots) != 0 || len(o.Policy.ReadRoots) != 1 {
		t.Errorf("start options: admitted=%v policy=%+v", o.Admitted, o.Policy)
	}
	if o.MaxSubagentDepth != 2 || o.MaxConcurrentSubagents != 4 {
		t.Errorf("caps = %d/%d", o.MaxSubagentDepth, o.MaxConcurrentSubagents)
	}
}

func TestFollowUpTurnResumesAndContinuesTheTranscript(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	first := h.send(h.msg("ask", "one", 0))
	h.waitStatus(first.Task.ID, core.TaskDone)
	evBefore, _ := h.store.ListTaskEvents(context.Background(), first.Task.ID, 0)

	req := h.msg("ask", "two", 0)
	req.TaskID = first.Task.ID
	second := h.send(req)
	if second.Task.ID != first.Task.ID || second.Turn == first.Turn {
		t.Fatalf("second = %+v", second)
	}
	waitFor(t, 10*time.Second, func() bool {
		st := h.state(first.Task.ID)
		return st.Task.Turns == 2 && st.Row.Status == core.TaskDone && st.Task.State.(coretasks.Idle).Turn == second.Turn
	}, "the second turn to finish")

	starts := h.adapter.Starts()
	if len(starts) != 2 || starts[1].ResumeSessionID == "" || starts[1].Prompt != "two" {
		t.Fatalf("starts = %+v", starts)
	}
	evAfter, _ := h.store.ListTaskEvents(context.Background(), first.Task.ID, 0)
	if len(evAfter) <= len(evBefore) || evAfter[len(evBefore)].Seq != int64(len(evBefore)+1) {
		t.Errorf("event numbering did not continue: %d then %d", len(evBefore), len(evAfter))
	}
	turns, _ := h.store.ListTaskTurns(context.Background(), first.Task.ID)
	if len(turns) != 2 || turns[1].Seq != 2 {
		t.Errorf("turns = %+v", turns)
	}
}

func TestMessageToABusyTaskIsRefused(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.gate = make(chan struct{})
	h.start()
	first := h.send(h.msg("ask", "one", 0))
	waitFor(t, 5*time.Second, func() bool { return len(h.adapter.Starts()) == 1 }, "the run to start")
	req := h.msg("ask", "two", 0)
	req.TaskID = first.Task.ID
	_, err := h.svc.Message(context.Background(), req)
	var rj *RejectedError
	if !errors.As(err, &rj) || rj.Reason != "task_busy" {
		t.Fatalf("got %v, want task_busy", err)
	}
	close(h.adapter.gate)
	h.waitStatus(first.Task.ID, core.TaskDone)
}

func TestCodeModeRunsInAWorktreeWithAPolicy(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.fixture = "edits"
	h.start()
	res := h.send(h.msg("code", "Add a health endpoint", 0))
	st := h.waitStatus(res.Task.ID, core.TaskReview)

	o := h.adapter.Starts()[0]
	if !strings.HasPrefix(o.Dir, filepath.Join(h.wtRoot, "wt")) && !strings.Contains(o.Dir, "wt") {
		t.Fatalf("code mode ran in %q, not a worktree", o.Dir)
	}
	if o.Dir == h.repos[0].Path {
		t.Fatal("code mode ran in the main checkout")
	}
	if len(o.Policy.WriteRoots) != 1 || len(o.Policy.ReadRoots) != 1 || !o.Admitted {
		t.Errorf("policy = %+v admitted=%v", o.Policy, o.Admitted)
	}
	wts, _ := h.store.ListWorktrees(context.Background(), res.Task.ID)
	if len(wts) != 1 || wts[0].RepoID != h.repos[0].ID {
		t.Errorf("worktrees = %+v", wts)
	}
	if idle := st.Task.State.(coretasks.Idle); idle.Outcome != (coretasks.ReadyForReview{}) {
		t.Errorf("outcome = %#v", idle.Outcome)
	}
}

func TestCurrentBranchNeedsOptIn(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.caps.Enforcement = agents.EnforcementHook
	h.start()
	req := h.msg("current_branch", "edit main", 0)
	req.OptIn = false
	_, err := h.svc.Message(context.Background(), req)
	var rj *RejectedError
	if !errors.As(err, &rj) || rj.Reason != "needs_opt_in" || rj.Message == "" {
		t.Fatalf("got %v", err)
	}
	// Nothing was created.
	if list, _ := h.store.ListTasks(context.Background(), h.project.ID); len(list) != 0 {
		t.Fatalf("a refused message left %d task(s)", len(list))
	}
	req.OptIn = true
	res := h.send(req)
	if res.Warning != "edits_main_checkout" {
		t.Errorf("warning = %q", res.Warning)
	}
	h.waitStatus(res.Task.ID, core.TaskReview)
	o := h.adapter.Starts()[0]
	if o.Dir != h.repos[0].Path && !samePath(o.Dir, h.repos[0].Path) {
		t.Errorf("ran in %q, want the main checkout %q", o.Dir, h.repos[0].Path)
	}
}

func samePath(a, b string) bool {
	ia, e1 := os.Stat(a)
	ib, e2 := os.Stat(b)
	return e1 == nil && e2 == nil && os.SameFile(ia, ib)
}

func TestBadInputIsAUserError(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	req := h.msg("ask", "   ", 0)
	_, err := h.svc.Message(context.Background(), req)
	var ue *coretasks.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("empty prompt: got %v", err)
	}
	req = h.msg("ask", "x", 0)
	req.Agent = "nope"
	if _, err := h.svc.Message(context.Background(), req); !errors.As(err, new(*RejectedError)) {
		t.Fatalf("unknown agent: got %v", err)
	}
	req = h.msg("ask", "x", 0)
	h.adapter.checkErr = errors.New("not signed in")
	if _, err := h.svc.Message(context.Background(), req); !errors.As(err, new(*RejectedError)) {
		t.Fatalf("check failure: got %v", err)
	}
}

func TestPanicInTheRunFailsTheTask(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.startPanic = true
	h.start()
	res := h.send(h.msg("ask", "boom", 0))
	st := h.waitStatus(res.Task.ID, core.TaskFailed)
	if idle := st.Task.State.(coretasks.Idle); idle.Outcome != (coretasks.Failed{Kind: coretasks.FailRun}) {
		t.Errorf("outcome = %#v", idle.Outcome)
	}
	if h.killer.count(res.Task.ID) == 0 {
		t.Error("no kill after a panic")
	}
}

func TestAgentThatCannotStartExplainsItself(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.adapter.startErr = errors.New("claude: command not found")
	h.start()
	res := h.send(h.msg("ask", "hi", 0))
	st := h.waitStatus(res.Task.ID, core.TaskFailed)
	if idle := st.Task.State.(coretasks.Idle); idle.Outcome != (coretasks.Failed{Kind: coretasks.FailStart}) {
		t.Errorf("outcome = %#v", idle.Outcome)
	}
	ev, _ := h.store.ListTaskEvents(context.Background(), res.Task.ID, 0)
	if len(ev) != 2 || !strings.Contains(string(ev[0].Data), "could not be started") {
		t.Fatalf("transcript = %v", ev)
	}
	if strings.Contains(string(ev[0].Data), "command not found") {
		t.Error("an error without a user message leaked into the transcript")
	}
}

func TestUsageIsBatchedAndTurnsAreTagged(t *testing.T) {
	// 250 ms is the production batch interval; with it a run's two usage events
	// arrive as one flush.
	h := newHarness(t, []string{"shop-api"})
	h.start()
	h.svc.c.UsageEvery = time.Hour // only the final flush
	res := h.send(h.msg("ask", "x", 0))
	st := h.waitStatus(res.Task.ID, core.TaskDone)
	turns, _ := h.store.ListTaskTurns(context.Background(), st.Row.ID)
	if turns[0].Usage.InputTokens != 150 {
		t.Errorf("usage = %+v", turns[0].Usage)
	}
}

var _ = agents.RunCompleted
