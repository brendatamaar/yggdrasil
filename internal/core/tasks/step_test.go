package tasks

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func names(cmds []Command) string {
	var out []string
	for _, c := range cmds {
		out = append(out, strings.TrimPrefix(fmt.Sprintf("%T", c), "tasks."))
	}
	return strings.Join(out, ",")
}

func okAdmission() Admission { return Admission{Admitted: true} }

func TestStepTransitionTable(t *testing.T) {
	caps := Caps{Depth: 1, Concurrent: 1}
	task := func(mode core.TaskMode, s State) Task {
		return Task{ID: "k", Mode: mode, Caps: caps, Turns: 1, State: s}
	}
	rows := []struct {
		name string
		from Task
		in   Input
		want State
		cmds string
	}{
		{"draft message admitted", task("", Draft{}),
			Message{Turn: "t1", Mode: core.ModeAsk, Prompt: "hi", Now: t0, Admission: okAdmission(), Caps: caps},
			Waiting{Turn: "t1", BlockedBy: BlockedNone{}}, "PersistTurn,Audit,Publish,Enqueue"},
		{"idle message admitted", task(core.ModeAsk, Idle{Outcome: Failed{Kind: FailRun}}),
			Message{Turn: "t2", Mode: core.ModeCode, Prompt: "hi", Now: t0, Admission: okAdmission(), Caps: caps},
			Waiting{Turn: "t2", BlockedBy: BlockedNone{}}, "PersistTurn,Audit,Publish,Enqueue"},
		{"draft message refused", task("", Draft{}),
			Message{Turn: "t1", Mode: core.ModeCode, Prompt: "hi", Admission: Admission{Reason: "untrusted", Message: "Trust the repo first."}},
			Draft{}, "Reject"},
		{"waiting admitted", task(core.ModeCode, Waiting{Turn: "t1"}), Admitted{Turn: "t1", Now: t0},
			Starting{Turn: "t1", Attempt: 1, Since: t0}, "PrepareRun,Audit,Publish"},
		{"waiting admitted again after denial", task(core.ModeCode, Waiting{Turn: "t1", Attempt: 1}), Admitted{Turn: "t1", Now: t0},
			Starting{Turn: "t1", Attempt: 2, Since: t0}, "PrepareRun,Audit,Publish"},
		{"waiting cancel", task(core.ModeCode, Waiting{Turn: "t1"}), CancelRequested{Now: t0},
			Idle{Outcome: Cancelled{}, Turn: "t1", At: t0}, "Dequeue,FinishTurn,Audit,Publish"},
		{"starting prepared", task(core.ModeCode, Starting{Turn: "t1", Attempt: 1, Since: t0}), Prepared{Turn: "t1", Attempt: 1, Dir: "w", Now: t0},
			Running{Turn: "t1", Attempt: 1, Since: t0}, "StartRun,Audit,Publish"},
		{"starting prepare failed", task(core.ModeCode, Starting{Turn: "t1", Attempt: 1, Since: t0}), PrepareFailed{Turn: "t1", Attempt: 1, Now: t0},
			Idle{Outcome: Failed{Kind: FailPrepare}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,Audit,Publish"},
		{"starting locks denied", task(core.ModeCurrentBranch, Starting{Turn: "t1", Attempt: 1, Since: t0}), LocksDenied{Turn: "t1", Attempt: 1, Repo: "r1", Holder: "build"},
			Waiting{Turn: "t1", Attempt: 1, BlockedBy: BlockedMainCheckout{Repo: "r1", Holder: "build"}}, "Requeue,Audit,Publish"},
		{"starting cancel", task(core.ModeCode, Starting{Turn: "t1", Attempt: 1, Since: t0}), CancelRequested{Now: t0},
			Stopping{Turn: "t1", Attempt: 1, Since: t0, Cause: CauseUserCancel, Phase: StopBeforeStart}, "KillRun,Audit"},
		{"running usage increment", task(core.ModeCode, Running{Turn: "t1", Attempt: 1, Usage: Usage{InputTokens: 2}}),
			UsageReported{Turn: "t1", Attempt: 1, Usage: Usage{InputTokens: 3}}, Running{Turn: "t1", Attempt: 1, Usage: Usage{InputTokens: 5}}, ""},
		{"running usage cumulative never lowers", task(core.ModeCode, Running{Turn: "t1", Attempt: 1, Usage: Usage{InputTokens: 9}}),
			UsageReported{Turn: "t1", Attempt: 1, Usage: Usage{InputTokens: 4}, Cumulative: true}, Running{Turn: "t1", Attempt: 1, Usage: Usage{InputTokens: 9}}, ""},
		{"subagent under cap", task(core.ModeCode, Running{Turn: "t1", Attempt: 1}), SubagentStarted{Turn: "t1", Attempt: 1, AgentID: "a", Depth: 1},
			Running{Turn: "t1", Attempt: 1, Subagents: []Subagent{{ID: "a", Depth: 1}}}, ""},
		{"subagent over depth", task(core.ModeCode, Running{Turn: "t1", Attempt: 1, Since: t0}), SubagentStarted{Turn: "t1", Attempt: 1, AgentID: "a", Depth: 2},
			Stopping{Turn: "t1", Attempt: 1, Since: t0, Cause: CauseCapExceeded}, "KillRun,Audit"},
		{"subagent over concurrency", task(core.ModeCode, Running{Turn: "t1", Attempt: 1, Since: t0, Subagents: []Subagent{{ID: "a", Depth: 1}}}),
			SubagentStarted{Turn: "t1", Attempt: 1, AgentID: "b", Depth: 1},
			Stopping{Turn: "t1", Attempt: 1, Since: t0, Cause: CauseCapExceeded}, "KillRun,Audit"},
		{"subagent stopped", task(core.ModeCode, Running{Turn: "t1", Attempt: 1, Subagents: []Subagent{{ID: "a", Depth: 1}}}),
			SubagentStopped{Turn: "t1", Attempt: 1, AgentID: "a"}, Running{Turn: "t1", Attempt: 1}, ""},
		{"finished ask", task(core.ModeAsk, Running{Turn: "t1", Attempt: 1}), RunFinished{Turn: "t1", Attempt: 1, Status: RunCompleted, Now: t0},
			Idle{Outcome: Answered{}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,KillTask,Audit,Publish"},
		{"finished code", task(core.ModeCode, Running{Turn: "t1", Attempt: 1}), RunFinished{Turn: "t1", Attempt: 1, Status: RunCompleted, Now: t0},
			Idle{Outcome: ReadyForReview{}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,KillTask,Audit,Publish"},
		{"finished failed", task(core.ModeCode, Running{Turn: "t1", Attempt: 1}), RunFinished{Turn: "t1", Attempt: 1, Status: RunFailed, Now: t0},
			Idle{Outcome: Failed{Kind: FailRun}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,KillTask,Audit,Publish"},
		{"finished start failed", task(core.ModeCode, Running{Turn: "t1", Attempt: 1}), RunFinished{Turn: "t1", Attempt: 1, Status: RunFailed, StartFail: true, Now: t0},
			Idle{Outcome: Failed{Kind: FailStart}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,KillTask,Audit,Publish"},
		{"finished cancelled", task(core.ModeCode, Running{Turn: "t1", Attempt: 1}), RunFinished{Turn: "t1", Attempt: 1, Status: RunCancelled, Now: t0},
			Idle{Outcome: Cancelled{}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,KillTask,Audit,Publish"},
		{"running cancel", task(core.ModeCode, Running{Turn: "t1", Attempt: 1, Since: t0}), CancelRequested{Now: t0},
			Stopping{Turn: "t1", Attempt: 1, Since: t0, Cause: CauseUserCancel}, "KillRun,Audit"},
		{"running integrity", task(core.ModeAsk, Running{Turn: "t1", Attempt: 1, Since: t0}), IntegrityViolated{Turn: "t1", Now: t0},
			Stopping{Turn: "t1", Attempt: 1, Since: t0, Cause: CauseIntegrityViolated}, "KillRun,Audit"},
		{"stopping cancel finished", task(core.ModeCode, Stopping{Turn: "t1", Attempt: 1, Cause: CauseUserCancel}), RunFinished{Turn: "t1", Attempt: 1, Status: RunCompleted, Now: t0},
			Idle{Outcome: Cancelled{}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,KillTask,Audit,Publish"},
		{"stopping integrity finished", task(core.ModeAsk, Stopping{Turn: "t1", Attempt: 1, Cause: CauseIntegrityViolated}), RunFinished{Turn: "t1", Attempt: 1, Status: RunCancelled, Now: t0},
			Idle{Outcome: NeedsYou{Why: NeedsIntegrityViolated}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,KillTask,Audit,Publish"},
		{"stopping cap finished", task(core.ModeCode, Stopping{Turn: "t1", Attempt: 1, Cause: CauseCapExceeded}), RunFinished{Turn: "t1", Attempt: 1, Status: RunFailed, Now: t0},
			Idle{Outcome: NeedsYou{Why: NeedsCapExceeded}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,KillTask,Audit,Publish"},
		{"starting recovered", task(core.ModeCode, Starting{Turn: "t1", Attempt: 1}), Recovered{Now: t0},
			Idle{Outcome: Failed{Kind: FailInterrupted}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,Audit,Publish"},
		{"running recovered", task(core.ModeCode, Running{Turn: "t1", Attempt: 1}), Recovered{Now: t0},
			Idle{Outcome: Failed{Kind: FailInterrupted}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,Audit,Publish"},
		{"stopping recovered", task(core.ModeCode, Stopping{Turn: "t1", Attempt: 1}), Recovered{Now: t0},
			Idle{Outcome: Failed{Kind: FailInterrupted}, Turn: "t1", At: t0}, "FinishTurn,ReleaseLocks,Audit,Publish"},
		{"review dismissed", task(core.ModeCode, Idle{Outcome: ReadyForReview{}}), DismissRequested{Now: t0},
			Idle{Outcome: Dismissed{}, At: t0}, "Audit,Publish"},
		{"needs you dismissed", task(core.ModeCode, Idle{Outcome: NeedsYou{Why: NeedsCapExceeded}}), DismissRequested{Now: t0},
			Idle{Outcome: Dismissed{}, At: t0}, "Audit,Publish"},
		// Rows that change nothing.
		{"idle cancel does nothing", task(core.ModeCode, Idle{Outcome: Answered{}}), CancelRequested{Now: t0}, Idle{Outcome: Answered{}}, ""},
		{"answered dismiss does nothing", task(core.ModeAsk, Idle{Outcome: Answered{}}), DismissRequested{Now: t0}, Idle{Outcome: Answered{}}, ""},
		{"draft dismiss does nothing", task("", Draft{}), DismissRequested{Now: t0}, Draft{}, ""},
		{"waiting recovered stays waiting", task(core.ModeCode, Waiting{Turn: "t1"}), Recovered{Now: t0}, Waiting{Turn: "t1"}, ""},
		{"idle recovered does nothing", task(core.ModeCode, Idle{Outcome: Cancelled{}}), Recovered{Now: t0}, Idle{Outcome: Cancelled{}}, ""},
		{"starting integrity does nothing", task(core.ModeCode, Starting{Turn: "t1", Attempt: 1}), IntegrityViolated{Turn: "t1"}, Starting{Turn: "t1", Attempt: 1}, ""},
		{"stopping cancel again does nothing", task(core.ModeCode, Stopping{Turn: "t1", Attempt: 1}), CancelRequested{}, Stopping{Turn: "t1", Attempt: 1}, ""},
		{"stopping processes gone does nothing", task(core.ModeCode, Stopping{Turn: "t1", Attempt: 1}), ProcessesGone{Turn: "t1", Attempt: 1}, Stopping{Turn: "t1", Attempt: 1}, ""},
		{"running message ignored", task(core.ModeCode, Running{Turn: "t1", Attempt: 1}),
			Message{Turn: "t2", Mode: core.ModeAsk, Prompt: "x", Admission: okAdmission()}, Running{Turn: "t1", Attempt: 1}, ""},
		{"waiting message ignored", task(core.ModeCode, Waiting{Turn: "t1"}),
			Message{Turn: "t2", Mode: core.ModeAsk, Prompt: "x", Admission: okAdmission()}, Waiting{Turn: "t1"}, ""},
		{"stale admitted", task(core.ModeCode, Waiting{Turn: "t1"}), Admitted{Turn: "t0"}, Waiting{Turn: "t1"}, ""},
		{"stale attempt prepared", task(core.ModeCode, Starting{Turn: "t1", Attempt: 2}), Prepared{Turn: "t1", Attempt: 1}, Starting{Turn: "t1", Attempt: 2}, ""},
		{"stale attempt finished", task(core.ModeCode, Running{Turn: "t1", Attempt: 2}), RunFinished{Turn: "t1", Attempt: 1, Status: RunCompleted}, Running{Turn: "t1", Attempt: 2}, ""},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			got, cmds := Step(c.from, c.in)
			if !reflect.DeepEqual(got.State, c.want) {
				t.Errorf("state = %#v, want %#v", got.State, c.want)
			}
			if names(cmds) != c.cmds {
				t.Errorf("commands = %s, want %s", names(cmds), c.cmds)
			}
		})
	}
}

func TestStepMessageDetails(t *testing.T) {
	d := Task{ID: "k", State: Draft{}}
	msg := Message{Turn: "t1", Mode: core.ModeCurrentBranch, Prompt: "secret prompt", Now: t0, Admission: okAdmission(),
		Caps: Caps{Depth: 2, Concurrent: 3}, EditsMain: []core.RepoID{"r1"}}
	got, cmds := Step(d, msg)
	if got.Mode != core.ModeCurrentBranch || got.Turns != 1 || got.Caps != msg.Caps {
		t.Errorf("task = %#v", got)
	}
	var enq Enqueue
	for _, c := range cmds {
		switch v := c.(type) {
		case Enqueue:
			enq = v
		case Audit:
			if strings.Contains(fmt.Sprint(v), "secret") {
				t.Errorf("the prompt leaked into the audit entry: %v", v)
			}
		case Publish:
			if v.Status != core.TaskQueued {
				t.Errorf("published %q", v.Status)
			}
		}
	}
	if len(enq.EditsMain) != 1 || enq.EditsMain[0] != "r1" {
		t.Errorf("Enqueue = %#v", enq)
	}

	// A mode that does not edit the main checkout asks for no lock.
	msg.Mode = core.ModeCode
	_, cmds = Step(d, msg)
	for _, c := range cmds {
		if e, ok := c.(Enqueue); ok && len(e.EditsMain) != 0 {
			t.Errorf("code turn asked for a lock: %#v", e)
		}
	}
}

func TestStepMessageRefusals(t *testing.T) {
	d := Task{ID: "k", State: Draft{}}
	base := Message{Turn: "t1", Mode: core.ModeAsk, Prompt: "x", Admission: okAdmission()}
	bad := map[string]func(m *Message){
		"turn id":           func(m *Message) { m.Turn = "" },
		"mode":              func(m *Message) { m.Mode = "yolo" },
		"long prompt":       func(m *Message) { m.Prompt = strings.Repeat("a", MaxPromptBytes+1) },
		"negative caps":     func(m *Message) { m.Caps.Depth = -1 },
		"no main repo":      func(m *Message) { m.Mode = core.ModeCurrentBranch },
		"bad edits repo":    func(m *Message) { m.Mode = core.ModeCurrentBranch; m.EditsMain = []core.RepoID{""} },
		"not admitted":      func(m *Message) { m.Admission = Admission{Reason: "x", Message: "no"} },
		"not admitted bare": func(m *Message) { m.Admission = Admission{} },
	}
	for name, mut := range bad {
		t.Run(name, func(t *testing.T) {
			m := base
			mut(&m)
			got, cmds := Step(d, m)
			if !sameTask(got, d) || names(cmds) != "Reject" {
				t.Errorf("got %#v %s", got, names(cmds))
			}
			if r := cmds[0].(Reject); r.Message == "" || r.Reason == "" {
				t.Errorf("Reject lacks reason or message: %#v", r)
			}
		})
	}
}

func TestStepStartRunDetails(t *testing.T) {
	tk := Task{ID: "k", Mode: core.ModeCode, SessionID: "sess", Caps: Caps{Depth: 1, Concurrent: 2},
		State: Starting{Turn: "t1", Attempt: 1, Since: t0}}
	_, cmds := Step(tk, Prepared{Turn: "t1", Attempt: 1, Dir: "w1", ExtraDirs: []string{"w2"}, Now: t0})
	sr, ok := cmds[0].(StartRun)
	if !ok {
		t.Fatalf("first command %T", cmds[0])
	}
	want := StartRun{Turn: "t1", Attempt: 1, Mode: core.ModeCode, Resume: true, Caps: tk.Caps, Dir: "w1", ExtraDirs: []string{"w2"}}
	if !reflect.DeepEqual(sr, want) {
		t.Errorf("StartRun = %#v, want %#v", sr, want)
	}
	tk.SessionID = ""
	_, cmds = Step(tk, Prepared{Turn: "t1", Attempt: 1, Dir: "w1"})
	if cmds[0].(StartRun).Resume {
		t.Error("Resume without a session")
	}
}

func TestStepFinishKeepsUsageAndSession(t *testing.T) {
	tk := Task{ID: "k", Mode: core.ModeCode, State: Running{Turn: "t1", Attempt: 1, Usage: Usage{InputTokens: 7, CostKnown: true, CostMicroUSD: 5}}}
	got, cmds := Step(tk, RunFinished{Turn: "t1", Attempt: 1, Status: RunCompleted, Usage: Usage{InputTokens: 4, OutputTokens: 2}, SessionID: "s9", Now: t0})
	f := cmds[0].(FinishTurn)
	if f.Usage.InputTokens != 7 || f.Usage.OutputTokens != 2 || f.Usage.CostMicroUSD != 5 || !f.Usage.CostKnown || f.SessionID != "s9" || got.SessionID != "s9" {
		t.Errorf("FinishTurn = %#v, task session %q", f, got.SessionID)
	}
}

func TestRunHelper(t *testing.T) {
	recs := Run(Task{ID: "k", State: Draft{}}, []Input{
		Message{Turn: "t1", Mode: core.ModeAsk, Prompt: "x", Now: t0, Admission: okAdmission(), Caps: Caps{Depth: 1, Concurrent: 1}},
		Admitted{Turn: "t1", Now: t0},
		Prepared{Turn: "t1", Attempt: 1, Dir: "w", Now: t0},
		RunFinished{Turn: "t1", Attempt: 1, Status: RunCompleted, Now: t0},
	})
	if got := recs[len(recs)-1].Next.State; !reflect.DeepEqual(got, Idle{Outcome: Answered{}, Turn: "t1", At: t0}) {
		t.Errorf("happy path ended in %#v", got)
	}
}
