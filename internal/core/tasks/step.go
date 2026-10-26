package tasks

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Step is (task, input) -> (task, commands): one task's turns, attempts,
// stop, finish, usage and caps (the Step table of docs/design/tasks.md). An
// input it does not handle in the current state, or one with a stale Turn or
// Attempt, changes nothing.
func Step(t Task, in Input) (Task, []Command) {
	switch v := in.(type) {
	case Message:
		return stepMessage(t, v)
	case Admitted:
		return stepAdmitted(t, v)
	case Prepared:
		return stepPrepared(t, v)
	case PrepareFailed:
		return stepPrepareFailed(t, v)
	case LocksDenied:
		return stepLocksDenied(t, v)
	case UsageReported:
		return stepUsage(t, v)
	case SubagentStarted:
		return stepSubagentStarted(t, v)
	case SubagentStopped:
		return stepSubagentStopped(t, v)
	case RunFinished:
		return stepRunFinished(t, v)
	case CancelRequested:
		return stepCancel(t, v)
	case IntegrityViolated:
		return stepIntegrity(t, v)
	case DismissRequested:
		return stepDismiss(t, v)
	case Recovered:
		return stepRecovered(t, v)
	}
	// ProcessesGone and anything unknown: nothing to decide.
	return t, nil
}

func (t Task) with(s State) Task {
	t.State = s
	return t
}

// change is the audit and publish pair of a status change (K13).
func change(action, outcome string, turn TurnID, status core.TaskStatus) []Command {
	return []Command{
		Audit{Entry: AuditEntry{Action: action, Outcome: outcome, Turn: turn}},
		Publish{Status: status},
	}
}

func stepMessage(t Task, m Message) (Task, []Command) {
	switch t.State.(type) {
	case Draft, Idle:
	default:
		return t, nil // one live turn at a time (K3)
	}
	if !m.Admission.Admitted {
		msg := m.Admission.Message
		if msg == "" {
			msg = "That message can't be sent."
		}
		reason := m.Admission.Reason
		if reason == "" {
			reason = "not_admitted"
		}
		return t, []Command{Reject{Reason: reason, Message: msg}}
	}
	// Defence in depth: the shell parsed the message, but the core never
	// starts a turn it cannot describe.
	if flaw := messageFlaw(m); flaw != "" {
		return t, []Command{Reject{Reason: "invalid_message", Message: flaw}}
	}
	var edits []core.RepoID
	if m.Mode == core.ModeCurrentBranch {
		edits = append(edits, m.EditsMain...)
	}
	next := t
	next.Mode = m.Mode
	next.Caps = narrowCaps(t.Caps, m.Caps)
	next.Turns++
	next.State = Waiting{Turn: m.Turn, BlockedBy: BlockedNone{}}
	cmds := []Command{
		PersistTurn{Turn: m.Turn, Mode: m.Mode, Prompt: m.Prompt},
		Audit{Entry: AuditEntry{Action: "task.turn", Outcome: "queued", Turn: m.Turn}},
		Publish{Status: core.TaskQueued},
		Enqueue{Turn: m.Turn, Mode: m.Mode, EditsMain: edits},
	}
	return next, cmds
}

// narrowCaps keeps the caps of the turn before when they are known (both
// above zero): a later message may keep or narrow them, never raise them. The
// shell loads the previous caps with the task; a zero Caps means "none yet",
// which also covers a shell that forgot to load them (see tasks.md).
func narrowCaps(prev, asked Caps) Caps {
	if prev.Depth < 1 || prev.Concurrent < 1 {
		return asked
	}
	return Caps{Depth: min(prev.Depth, asked.Depth), Concurrent: min(prev.Concurrent, asked.Concurrent)}
}

func messageFlaw(m Message) string {
	switch {
	case m.Turn.Validate() != nil:
		return "That message has no valid turn ID."
	case m.Mode.Validate() != nil:
		return "That message has an unknown mode."
	case len(m.Prompt) > MaxPromptBytes:
		return "That message is too long."
	case m.Caps.Depth < 1 || m.Caps.Concurrent < 1 ||
		m.Caps.Depth > MaxSubagentDepth || m.Caps.Concurrent > MaxSubagentConcurrent:
		return "That message has invalid sub-agent limits."
	case m.Mode == core.ModeCurrentBranch && len(m.EditsMain) == 0:
		return "A current-branch task must name the repo it edits."
	}
	for _, r := range m.EditsMain {
		if r.Validate() != nil {
			return "That message names an invalid repo."
		}
	}
	return ""
}

func stepAdmitted(t Task, a Admitted) (Task, []Command) {
	w, ok := t.State.(Waiting)
	if !ok || w.Turn != a.Turn {
		return t, nil
	}
	n := w.Attempt + 1
	cmds := append([]Command{PrepareRun{Turn: w.Turn, Attempt: n}}, change("task.start", "preparing", w.Turn, core.TaskRunning)...)
	return t.with(Starting{Turn: w.Turn, Attempt: n, Since: a.Now}), cmds
}

func stepPrepared(t Task, p Prepared) (Task, []Command) {
	if st, ok := t.State.(Stopping); ok {
		if st.Phase != StopBeforeStart || st.Turn != p.Turn || st.Attempt != p.Attempt {
			return t, nil // a stop from Running ends only on RunFinished
		}
		// The stop raced the prepare: nothing was started, so the stop ends
		// here (C2). KillTask is harmless and covers a half-started process.
		return toIdle(t, st.Turn, st.Attempt, st.Usage, "", st.Cause.outcome(), p.Now, true)
	}
	s, ok := t.State.(Starting)
	if !ok || s.Turn != p.Turn || s.Attempt != p.Attempt {
		return t, nil
	}
	cmds := append([]Command{StartRun{
		Turn: s.Turn, Attempt: s.Attempt, Mode: t.Mode, Resume: t.SessionID != "",
		Caps: t.Caps, Dir: p.Dir, ExtraDirs: append([]string(nil), p.ExtraDirs...),
	}}, change("task.run", "running", s.Turn, core.TaskRunning)...)
	return t.with(Running{Turn: s.Turn, Attempt: s.Attempt, Since: s.Since}), cmds
}

// toIdle ends the live turn: FinishTurn, ReleaseLocks, KillTask (when a
// process may exist), audit and publish.
func toIdle(t Task, turn TurnID, attempt Attempt, usage Usage, session string, o Outcome, now time.Time, kill bool) (Task, []Command) {
	if session != "" {
		t.SessionID = session
	}
	cmds := []Command{
		FinishTurn{Turn: turn, Attempt: attempt, Usage: usage, SessionID: t.SessionID},
		ReleaseLocks{},
	}
	if kill {
		cmds = append(cmds, KillTask{})
	}
	cmds = append(cmds, change("task.finish", OutcomeCode(o), turn, o.status())...)
	return t.with(Idle{Outcome: o, Turn: turn, At: now}), cmds
}

func stepPrepareFailed(t Task, p PrepareFailed) (Task, []Command) {
	if st, ok := t.State.(Stopping); ok {
		if st.Phase != StopBeforeStart || st.Turn != p.Turn || st.Attempt != p.Attempt {
			return t, nil
		}
		return toIdle(t, st.Turn, st.Attempt, st.Usage, "", st.Cause.outcome(), p.Now, false)
	}
	s, ok := t.State.(Starting)
	if !ok || s.Turn != p.Turn || s.Attempt != p.Attempt {
		return t, nil
	}
	return toIdle(t, s.Turn, s.Attempt, Usage{}, "", Failed{Kind: FailPrepare}, p.Now, false)
}

func stepLocksDenied(t Task, l LocksDenied) (Task, []Command) {
	s, ok := t.State.(Starting)
	if !ok || s.Turn != l.Turn || s.Attempt != l.Attempt {
		return t, nil
	}
	w := Waiting{Turn: s.Turn, Attempt: s.Attempt, BlockedBy: BlockedMainCheckout{Repo: l.Repo, Holder: l.Holder}}
	cmds := append([]Command{Requeue{Turn: s.Turn}}, change("task.requeue", "main_checkout_busy", s.Turn, core.TaskQueued)...)
	return t.with(w), cmds
}

func stepUsage(t Task, u UsageReported) (Task, []Command) {
	switch s := t.State.(type) {
	case Running:
		if s.Turn != u.Turn || s.Attempt != u.Attempt {
			return t, nil
		}
		s.Usage = mergeUsage(s.Usage, u.Usage, u.Cumulative)
		return t.with(s), nil
	case Stopping:
		if s.Turn != u.Turn || s.Attempt != u.Attempt {
			return t, nil
		}
		s.Usage = mergeUsage(s.Usage, u.Usage, u.Cumulative)
		return t.with(s), nil
	}
	return t, nil
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// mergeUsage never lowers a total (K8): an increment adds, cumulative totals
// raise each field to the new value; negative fields count as zero.
func mergeUsage(cur, in Usage, cumulative bool) Usage {
	if cumulative {
		return Usage{
			InputTokens:      max(cur.InputTokens, nonNeg(in.InputTokens)),
			OutputTokens:     max(cur.OutputTokens, nonNeg(in.OutputTokens)),
			CacheWriteTokens: max(cur.CacheWriteTokens, nonNeg(in.CacheWriteTokens)),
			CacheReadTokens:  max(cur.CacheReadTokens, nonNeg(in.CacheReadTokens)),
			CostMicroUSD:     max(cur.CostMicroUSD, nonNeg(in.CostMicroUSD)),
			CostKnown:        cur.CostKnown || in.CostKnown,
			DurationMS:       max(cur.DurationMS, nonNeg(in.DurationMS)),
		}
	}
	return Usage{
		InputTokens:      cur.InputTokens + nonNeg(in.InputTokens),
		OutputTokens:     cur.OutputTokens + nonNeg(in.OutputTokens),
		CacheWriteTokens: cur.CacheWriteTokens + nonNeg(in.CacheWriteTokens),
		CacheReadTokens:  cur.CacheReadTokens + nonNeg(in.CacheReadTokens),
		CostMicroUSD:     cur.CostMicroUSD + nonNeg(in.CostMicroUSD),
		CostKnown:        cur.CostKnown || in.CostKnown,
		DurationMS:       cur.DurationMS + nonNeg(in.DurationMS),
	}
}

func stepSubagentStarted(t Task, e SubagentStarted) (Task, []Command) {
	r, ok := t.State.(Running)
	if !ok || r.Turn != e.Turn || r.Attempt != e.Attempt {
		return t, nil
	}
	active := false
	for _, a := range r.Subagents {
		if a.ID == e.AgentID {
			active = true
		}
	}
	// A report with no ID or a depth below 1 cannot be checked, so it counts
	// as over the cap (fail closed).
	over := e.AgentID == "" || e.Depth < 1 || e.Depth > t.Caps.Depth || (!active && len(r.Subagents) >= t.Caps.Concurrent)
	if over {
		cmds := []Command{
			KillRun{Turn: r.Turn, Attempt: r.Attempt},
			Audit{Entry: AuditEntry{Action: "subagent.cap", Outcome: "stopped", Turn: r.Turn}},
		}
		return t.with(Stopping{Turn: r.Turn, Attempt: r.Attempt, Since: r.Since, Cause: CauseCapExceeded, Usage: r.Usage}), cmds
	}
	if active {
		return t, nil
	}
	r.Subagents = append(append([]Subagent(nil), r.Subagents...), Subagent{ID: e.AgentID, Depth: e.Depth})
	return t.with(r), nil
}

func stepSubagentStopped(t Task, e SubagentStopped) (Task, []Command) {
	r, ok := t.State.(Running)
	if !ok || r.Turn != e.Turn || r.Attempt != e.Attempt {
		return t, nil
	}
	var kept []Subagent
	for _, a := range r.Subagents {
		if a.ID != e.AgentID {
			kept = append(kept, a)
		}
	}
	r.Subagents = kept
	return t.with(r), nil
}

func stepRunFinished(t Task, f RunFinished) (Task, []Command) {
	switch s := t.State.(type) {
	case Running:
		if s.Turn != f.Turn || s.Attempt != f.Attempt {
			return t, nil
		}
		var o Outcome
		switch f.Status {
		case RunCompleted:
			o = ReadyForReview{}
			if t.Mode == core.ModeAsk {
				o = Answered{}
			}
		case RunCancelled:
			o = Cancelled{}
		default:
			o = Failed{Kind: FailRun}
			if f.StartFail {
				o = Failed{Kind: FailStart}
			}
		}
		return toIdle(t, s.Turn, s.Attempt, mergeUsage(s.Usage, f.Usage, true), f.SessionID, o, f.Now, true)
	case Stopping:
		if s.Turn != f.Turn || s.Attempt != f.Attempt {
			return t, nil
		}
		return toIdle(t, s.Turn, s.Attempt, mergeUsage(s.Usage, f.Usage, true), f.SessionID, s.Cause.outcome(), f.Now, true)
	}
	return t, nil
}

func stepCancel(t Task, c CancelRequested) (Task, []Command) {
	switch s := t.State.(type) {
	case Waiting:
		cmds := []Command{
			Dequeue{Turn: s.Turn},
			FinishTurn{Turn: s.Turn, Attempt: s.Attempt, SessionID: t.SessionID},
		}
		cmds = append(cmds, change("task.finish", OutcomeCode(Cancelled{}), s.Turn, core.TaskCancelled)...)
		return t.with(Idle{Outcome: Cancelled{}, Turn: s.Turn, At: c.Now}), cmds
	case Starting:
		return t.with(Stopping{Turn: s.Turn, Attempt: s.Attempt, Since: s.Since, Cause: CauseUserCancel, Phase: StopBeforeStart}), stopCmds(s.Turn, s.Attempt, "task.cancel")
	case Running:
		return t.with(Stopping{Turn: s.Turn, Attempt: s.Attempt, Since: s.Since, Cause: CauseUserCancel, Usage: s.Usage}), stopCmds(s.Turn, s.Attempt, "task.cancel")
	}
	return t, nil
}

func stopCmds(turn TurnID, attempt Attempt, action string) []Command {
	return []Command{
		KillRun{Turn: turn, Attempt: attempt},
		Audit{Entry: AuditEntry{Action: action, Outcome: "stopping", Turn: turn}},
	}
}

// stepIntegrity handles guard.StepIntegrity's verdict. It is never dropped
// for the turn it names (SF-07): a running turn is stopped, a stop in progress
// is upgraded to the integrity cause, and a turn that already finished as
// any other outcome (answered, ready for review, cancelled, failed, dismissed,
// another needs_you) becomes needs_you{integrity_violated}. The shell should hold
// RunFinished until the After snapshot is checked; the Idle row is the safety
// net when it does not.
func stepIntegrity(t Task, v IntegrityViolated) (Task, []Command) {
	switch s := t.State.(type) {
	case Running:
		if s.Turn != v.Turn {
			return t, nil
		}
		return t.with(Stopping{Turn: s.Turn, Attempt: s.Attempt, Since: s.Since, Cause: CauseIntegrityViolated, Usage: s.Usage}),
			stopCmds(s.Turn, s.Attempt, "task.integrity")
	case Stopping:
		if s.Turn != v.Turn || s.Cause == CauseIntegrityViolated {
			return t, nil
		}
		s.Cause = CauseIntegrityViolated
		return t.with(s), []Command{Audit{Entry: AuditEntry{Action: "task.integrity", Outcome: "stopping", Turn: s.Turn}}}
	case Idle:
		if v.Turn == "" || s.Turn != v.Turn {
			return t, nil
		}
		o := NeedsYou{Why: NeedsIntegrityViolated}
		if s.Outcome == Outcome(o) {
			return t, nil
		}
		return t.with(Idle{Outcome: o, Turn: s.Turn, At: v.Now}),
			change("task.integrity", OutcomeCode(o), s.Turn, o.status())
	}
	return t, nil
}

func stepDismiss(t Task, d DismissRequested) (Task, []Command) {
	i, ok := t.State.(Idle)
	if !ok {
		return t, nil
	}
	switch i.Outcome.(type) {
	case ReadyForReview, NeedsYou:
		return t.with(Idle{Outcome: Dismissed{}, Turn: i.Turn, At: d.Now}), change("task.dismiss", OutcomeCode(Dismissed{}), "", core.TaskDone)
	}
	return t, nil
}

func stepRecovered(t Task, r Recovered) (Task, []Command) {
	var turn TurnID
	var att Attempt
	var u Usage
	switch s := t.State.(type) {
	case Starting:
		turn, att = s.Turn, s.Attempt
	case Running:
		turn, att, u = s.Turn, s.Attempt, s.Usage
	case Stopping:
		turn, att, u = s.Turn, s.Attempt, s.Usage
		// A stop in a read-only turn may hide an integrity violation that was
		// lost with the process, and the stored row has no cause. Conservative:
		// the user reviews it (a plain user-cancel of an Ask turn is mislabelled
		// on purpose). Other causes end as interrupted.
		if t.Mode == core.ModeAsk || s.Cause == CauseIntegrityViolated {
			return toIdle(t, turn, att, u, "", NeedsYou{Why: NeedsIntegrityViolated}, r.Now, false)
		}
	default:
		return t, nil
	}
	// The usage here is what the state holds; the shell replaces it with the
	// sum of the stored events before it commits.
	return toIdle(t, turn, att, u, "", Failed{Kind: FailInterrupted}, r.Now, false)
}
