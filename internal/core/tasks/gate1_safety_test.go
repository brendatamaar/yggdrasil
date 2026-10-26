package tasks

import (
	"fmt"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// M2a (SF-07, invariant 9): an integrity violation for the last turn is never
// dropped, even when the turn already finished.
func TestIntegrityAfterFinish(t *testing.T) {
	needs := NeedsYou{Why: NeedsIntegrityViolated}
	for _, o := range []Outcome{Answered{}, ReadyForReview{}, Cancelled{}, Failed{Kind: FailRun}, Dismissed{}, NeedsYou{Why: NeedsCapExceeded}} {
		tk := Task{ID: "k", Mode: core.ModeAsk, Turns: 1, State: Idle{Outcome: o, Turn: "t1", At: t0}}
		now := t0.Add(5)
		got, cmds := Step(tk, IntegrityViolated{Turn: "t1", Now: now})
		want := Idle{Outcome: needs, Turn: "t1", At: now}
		if got.State != want {
			t.Errorf("%T: state %#v, want %#v", o, got.State, want)
		}
		if names(cmds) != "Audit,Publish" || !publishes(cmds, core.TaskNeedsYou) {
			t.Errorf("%T: commands %s", o, names(cmds))
		}
	}
	// Not the last turn, no turn, or already an integrity needs_you: unchanged.
	for _, tc := range []struct {
		name string
		s    Idle
		in   IntegrityViolated
	}{
		{"other turn", Idle{Outcome: Answered{}, Turn: "t1"}, IntegrityViolated{Turn: "t2"}},
		{"legacy idle without turn", Idle{Outcome: Answered{}}, IntegrityViolated{Turn: "t1"}},
		{"empty turn on both", Idle{Outcome: Answered{}}, IntegrityViolated{}},
		{"already integrity", Idle{Outcome: NeedsYou{Why: NeedsIntegrityViolated}, Turn: "t1"}, IntegrityViolated{Turn: "t1"}},
	} {
		tk := Task{ID: "k", Mode: core.ModeAsk, Turns: 1, State: tc.s}
		got, cmds := Step(tk, tc.in)
		if got.State != tc.s || len(cmds) != 0 {
			t.Errorf("%s: changed to %#v with %s", tc.name, got.State, names(cmds))
		}
	}
}

// A violation that arrives while the task is already stopping for another
// reason is not lost: the stop ends as needs_you, not as cancelled or cap.
func TestIntegrityDuringStopping(t *testing.T) {
	for _, cause := range []StopCause{CauseUserCancel, CauseCapExceeded} {
		tk := Task{ID: "k", Mode: core.ModeAsk, Turns: 1, State: Stopping{Turn: "t1", Attempt: 1, Since: t0, Cause: cause}}
		got, cmds := Step(tk, IntegrityViolated{Turn: "t1", Now: t0})
		if s, ok := got.State.(Stopping); !ok || s.Cause != CauseIntegrityViolated {
			t.Fatalf("%s: state %#v", cause, got.State)
		}
		if hasCmd[KillRun](cmds) || !hasCmd[Audit](cmds) {
			t.Fatalf("%s: commands %s", cause, names(cmds))
		}
		fin, _ := Step(got, RunFinished{Turn: "t1", Attempt: 1, Status: RunCancelled, Now: t0})
		if i, ok := fin.State.(Idle); !ok || i.Outcome != (NeedsYou{Why: NeedsIntegrityViolated}) || i.Turn != "t1" {
			t.Fatalf("%s: finished as %#v", cause, fin.State)
		}
	}
}

func TestIdleRemembersLastTurn(t *testing.T) {
	tk := Task{ID: "k", Mode: core.ModeCode, Turns: 1, State: Running{Turn: "t1", Attempt: 1}}
	got, _ := Step(tk, RunFinished{Turn: "t1", Attempt: 1, Status: RunCompleted, Now: t0})
	if i, ok := got.State.(Idle); !ok || i.Turn != "t1" {
		t.Fatalf("idle after finish: %#v", got.State)
	}
	got, _ = Step(Task{ID: "k", Turns: 1, State: Waiting{Turn: "t2"}}, CancelRequested{Now: t0})
	if i, ok := got.State.(Idle); !ok || i.Turn != "t2" {
		t.Fatalf("idle after cancel in queue: %#v", got.State)
	}
	got, _ = Step(Task{ID: "k", Turns: 1, State: Idle{Outcome: ReadyForReview{}, Turn: "t3"}}, DismissRequested{Now: t0})
	if i, ok := got.State.(Idle); !ok || i.Turn != "t3" {
		t.Fatalf("idle after dismiss: %#v", got.State)
	}
}

// M2a property: once a turn is known to have changed the tree (an integrity
// violation arrived while the turn was running, stopping or just finished),
// the task never shows that turn as answered or ready for review.
func TestPropViolatedTurnNeverAnswered(t *testing.T) {
	drive(t, func() checker {
		violated := map[TurnID]bool{}
		return func(prev Task, in Input, next Task, cmds []Command) error {
			if v, ok := in.(IntegrityViolated); ok && v.Turn != "" {
				switch s := prev.State.(type) {
				case Running:
					violated[v.Turn] = violated[v.Turn] || s.Turn == v.Turn
				case Stopping:
					violated[v.Turn] = violated[v.Turn] || s.Turn == v.Turn
				case Idle:
					violated[v.Turn] = violated[v.Turn] || s.Turn == v.Turn
				}
			}
			if i, ok := next.State.(Idle); ok && i.Turn != "" && violated[i.Turn] {
				switch i.Outcome.(type) {
				case Answered, ReadyForReview:
					return fmt.Errorf("turn %s changed the tree but ended as %T (status %q)", i.Turn, i.Outcome, i.Status())
				}
			}
			return nil
		}
	})
}

// M4: caps. A message outside [1, max] is refused; a later message never
// raises the caps of the turn before it.
func TestMessageCaps(t *testing.T) {
	msg := func(c Caps) Message {
		return Message{Turn: "t2", Mode: core.ModeAsk, Prompt: "hi", Now: t0, Admission: okAdmission(), Caps: c}
	}
	bad := []Caps{
		{0, 1}, {1, 0}, {-1, 1}, {1, -1}, {0, 0},
		{MaxSubagentDepth + 1, 1}, {1, MaxSubagentConcurrent + 1},
	}
	for _, c := range bad {
		got, cmds := Step(Task{ID: "k", State: Draft{}}, msg(c))
		if _, ok := got.State.(Draft); !ok || !hasCmd[Reject](cmds) {
			t.Errorf("caps %+v accepted: %#v", c, got.State)
		}
	}
	top := Caps{MaxSubagentDepth, MaxSubagentConcurrent}
	if got, _ := Step(Task{ID: "k", State: Draft{}}, msg(top)); got.Caps != top {
		t.Errorf("maximum caps not kept: %+v", got.Caps)
	}
	prev := Task{ID: "k", Turns: 1, Caps: Caps{2, 3}, State: Idle{Outcome: Answered{}, Turn: "t1"}}
	for _, tc := range []struct{ in, want Caps }{
		{Caps{2, 3}, Caps{2, 3}},
		{Caps{1, 2}, Caps{1, 2}},
		{Caps{4, 8}, Caps{2, 3}},
		{Caps{4, 1}, Caps{2, 1}},
		{Caps{1, 8}, Caps{1, 3}},
	} {
		got, _ := Step(prev, msg(tc.in))
		if got.Caps != tc.want {
			t.Errorf("after %+v, caps %+v, want %+v", tc.in, got.Caps, tc.want)
		}
	}
}

func TestPropCapsBoundedAndNeverGrow(t *testing.T) {
	drive(t, func() checker {
		return func(prev Task, in Input, next Task, cmds []Command) error {
			if next.Caps.Depth > MaxSubagentDepth || next.Caps.Concurrent > MaxSubagentConcurrent {
				return fmt.Errorf("caps %+v above the platform maximum", next.Caps)
			}
			if next.Turns > prev.Turns {
				if next.Caps.Depth < 1 || next.Caps.Concurrent < 1 {
					return fmt.Errorf("a turn started with caps %+v", next.Caps)
				}
				known := prev.Caps.Depth > 0 && prev.Caps.Concurrent > 0
				if known && (next.Caps.Depth > prev.Caps.Depth || next.Caps.Concurrent > prev.Caps.Concurrent) {
					return fmt.Errorf("caps grew from %+v to %+v", prev.Caps, next.Caps)
				}
			} else if next.Caps != prev.Caps {
				return fmt.Errorf("caps changed without a new turn: %+v -> %+v", prev.Caps, next.Caps)
			}
			for _, c := range cmds {
				if s, ok := c.(StartRun); ok && s.Caps != next.Caps {
					return fmt.Errorf("StartRun caps %+v differ from the task's %+v", s.Caps, next.Caps)
				}
			}
			return nil
		}
	})
}

// C2: a prepare result that arrives while stopping ends the stop, so a shell
// that cannot deliver ProcessesGone cannot leave the task stuck.
func TestStoppingEndsOnPrepareResult(t *testing.T) {
	for cause, want := range map[StopCause]Outcome{
		CauseUserCancel:        Cancelled{},
		CauseIntegrityViolated: NeedsYou{Why: NeedsIntegrityViolated},
		CauseCapExceeded:       NeedsYou{Why: NeedsCapExceeded},
	} {
		for _, in := range []Input{
			PrepareFailed{Turn: "t1", Attempt: 1, Reason: "git", Now: t0},
			Prepared{Turn: "t1", Attempt: 1, Dir: "w", Now: t0},
		} {
			tk := Task{ID: "k", Mode: core.ModeCode, Turns: 1,
				State: Stopping{Turn: "t1", Attempt: 1, Since: t0, Cause: cause, Phase: StopBeforeStart, Usage: Usage{InputTokens: 4}}}
			got, cmds := Step(tk, in)
			if got.State != (Idle{Outcome: want, Turn: "t1", At: t0}) {
				t.Errorf("%s/%T: state %#v", cause, in, got.State)
			}
			if !hasCmd[FinishTurn](cmds) || !hasCmd[ReleaseLocks](cmds) || hasCmd[StartRun](cmds) || !publishes(cmds, want.status()) {
				t.Errorf("%s/%T: commands %s", cause, in, names(cmds))
			}
			for _, c := range cmds {
				if f, ok := c.(FinishTurn); ok && f.Usage.InputTokens != 4 {
					t.Errorf("%s/%T: usage lost: %+v", cause, in, f.Usage)
				}
			}
			// Stale attempt or turn: ignored.
			for _, stale := range []Input{
				PrepareFailed{Turn: "t1", Attempt: 2, Now: t0}, PrepareFailed{Turn: "x", Attempt: 1, Now: t0},
				Prepared{Turn: "t1", Attempt: 2, Now: t0}, Prepared{Turn: "x", Attempt: 1, Now: t0},
			} {
				if g, c := Step(tk, stale); !sameTask(g, tk) || len(c) != 0 {
					t.Errorf("stale %#v changed a stopping task", stale)
				}
			}
		}
	}
	// ProcessesGone stays ignored.
	tk := Task{ID: "k", Turns: 1, State: Stopping{Turn: "t1", Attempt: 1, Since: t0, Cause: CauseUserCancel}}
	if g, c := Step(tk, ProcessesGone{Turn: "t1", Attempt: 1}); !sameTask(g, tk) || len(c) != 0 {
		t.Errorf("ProcessesGone changed a stopping task")
	}
}

// S1: Prepared/PrepareFailed end a stop only when the process never started. A
// stop that came from Running ignores a duplicate prepare result; only
// RunFinished ends it.
func TestStoppingFromRunningIgnoresPrepareResult(t *testing.T) {
	run := Task{ID: "k", Mode: core.ModeCode, Turns: 1, State: Running{Turn: "t1", Attempt: 1, Since: t0}}
	stopped, _ := Step(run, CancelRequested{Now: t0})
	for _, in := range []Input{
		Prepared{Turn: "t1", Attempt: 1, Dir: "w", Now: t0},
		PrepareFailed{Turn: "t1", Attempt: 1, Reason: "dup", Now: t0},
	} {
		got, cmds := Step(stopped, in)
		if !sameTask(got, stopped) || len(cmds) != 0 {
			t.Errorf("%T ended a stop that came from Running: %#v %s", in, got.State, names(cmds))
		}
	}
	if fin, _ := Step(stopped, RunFinished{Turn: "t1", Attempt: 1, Status: RunCancelled, Now: t0}); fin.State.Status() != core.TaskCancelled {
		t.Errorf("RunFinished did not end the stop: %#v", fin.State)
	}
	// A stop from Starting does end on the prepare result.
	st := Task{ID: "k", Mode: core.ModeCode, Turns: 1, State: Starting{Turn: "t1", Attempt: 1, Since: t0}}
	stopped, _ = Step(st, CancelRequested{Now: t0})
	if s, ok := stopped.State.(Stopping); !ok || s.Phase != StopBeforeStart {
		t.Fatalf("cancel in Starting gave %#v", stopped.State)
	}
	if got, _ := Step(stopped, Prepared{Turn: "t1", Attempt: 1, Now: t0}); got.State.Status() != core.TaskCancelled {
		t.Errorf("Prepared did not end a stop before start: %#v", got.State)
	}
}

// S5: recovering a Stopping task must not lose an integrity violation.
func TestRecoveredStoppingKeepsIntegrity(t *testing.T) {
	needs := NeedsYou{Why: NeedsIntegrityViolated}
	for _, tc := range []struct {
		name string
		mode core.TaskMode
		s    Stopping
		want Outcome
	}{
		{"ask user cancel", core.ModeAsk, Stopping{Turn: "t1", Attempt: 1, Cause: CauseUserCancel}, needs},
		{"ask cap", core.ModeAsk, Stopping{Turn: "t1", Attempt: 1, Cause: CauseCapExceeded}, needs},
		{"code integrity cause", core.ModeCode, Stopping{Turn: "t1", Attempt: 1, Cause: CauseIntegrityViolated}, needs},
		{"code user cancel", core.ModeCode, Stopping{Turn: "t1", Attempt: 1, Cause: CauseUserCancel}, Failed{Kind: FailInterrupted}},
		{"code cap", core.ModeCode, Stopping{Turn: "t1", Attempt: 1, Cause: CauseCapExceeded}, Failed{Kind: FailInterrupted}},
	} {
		got, cmds := Step(Task{ID: "k", Mode: tc.mode, Turns: 1, State: tc.s}, Recovered{Now: t0})
		if got.State != (Idle{Outcome: tc.want, Turn: "t1", At: t0}) {
			t.Errorf("%s: %#v", tc.name, got.State)
		}
		if !publishes(cmds, tc.want.status()) || !hasCmd[FinishTurn](cmds) {
			t.Errorf("%s: %s", tc.name, names(cmds))
		}
	}
	// Starting and Running keep Failed(Interrupted), even in Ask mode.
	for _, s := range []State{Starting{Turn: "t1", Attempt: 1}, Running{Turn: "t1", Attempt: 1}} {
		got, _ := Step(Task{ID: "k", Mode: core.ModeAsk, Turns: 1, State: s}, Recovered{Now: t0})
		if got.State != (Idle{Outcome: Failed{Kind: FailInterrupted}, Turn: "t1", At: t0}) {
			t.Errorf("%T: %#v", s, got.State)
		}
	}
}

// A sub-agent report with no ID or a depth below 1 counts as over the cap.
func TestSubagentStartedMalformedFailsClosed(t *testing.T) {
	run := Task{ID: "k", Mode: core.ModeCode, Turns: 1, Caps: Caps{Depth: 2, Concurrent: 3},
		State: Running{Turn: "t1", Attempt: 1, Since: t0}}
	for _, e := range []SubagentStarted{
		{Turn: "t1", Attempt: 1, AgentID: "", Depth: 1},
		{Turn: "t1", Attempt: 1, AgentID: "a", Depth: 0},
		{Turn: "t1", Attempt: 1, AgentID: "a", Depth: -1},
	} {
		got, cmds := Step(run, e)
		if s, ok := got.State.(Stopping); !ok || s.Cause != CauseCapExceeded || !hasCmd[KillRun](cmds) {
			t.Errorf("%+v: %#v %s", e, got.State, names(cmds))
		}
	}
}
