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
	next.Caps = m.Caps
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

func messageFlaw(m Message) string {
	switch {
	case m.Turn.Validate() != nil:
		return "That message has no valid turn ID."
	case m.Mode.Validate() != nil:
		return "That message has an unknown mode."
	case len(m.Prompt) > MaxPromptBytes:
		return "That message is too long."
	case m.Caps.Depth < 0 || m.Caps.Concurrent < 0:
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
	return t.with(Idle{Outcome: o, At: now}), cmds
}

func stepPrepareFailed(t Task, p PrepareFailed) (Task, []Command) {
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
		if s.Attempt != u.Attempt {
			return t, nil
		}
		s.Usage = mergeUsage(s.Usage, u.Usage, u.Cumulative)
		return t.with(s), nil
	case Stopping:
		if s.Attempt != u.Attempt {
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
	if !ok || r.Attempt != e.Attempt {
		return t, nil
	}
	active := false
	for _, a := range r.Subagents {
		if a.ID == e.AgentID {
			active = true
		}
	}
	over := e.Depth > t.Caps.Depth || (!active && len(r.Subagents) >= t.Caps.Concurrent)
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
	if !ok || r.Attempt != e.Attempt {
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
		var o Outcome
		switch s.Cause {
		case CauseIntegrityViolated:
			o = NeedsYou{Why: NeedsIntegrityViolated}
		case CauseCapExceeded:
			o = NeedsYou{Why: NeedsCapExceeded}
		default:
			o = Cancelled{}
		}
		return toIdle(t, s.Turn, s.Attempt, mergeUsage(s.Usage, f.Usage, true), f.SessionID, o, f.Now, true)
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
		return t.with(Idle{Outcome: Cancelled{}, At: c.Now}), cmds
	case Starting:
		return t.with(Stopping{Turn: s.Turn, Attempt: s.Attempt, Since: s.Since, Cause: CauseUserCancel}), stopCmds(s.Turn, s.Attempt, "task.cancel")
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

func stepIntegrity(t Task, v IntegrityViolated) (Task, []Command) {
	r, ok := t.State.(Running)
	if !ok || r.Turn != v.Turn {
		return t, nil
	}
	return t.with(Stopping{Turn: r.Turn, Attempt: r.Attempt, Since: r.Since, Cause: CauseIntegrityViolated, Usage: r.Usage}),
		stopCmds(r.Turn, r.Attempt, "task.integrity")
}

func stepDismiss(t Task, d DismissRequested) (Task, []Command) {
	i, ok := t.State.(Idle)
	if !ok {
		return t, nil
	}
	switch i.Outcome.(type) {
	case ReadyForReview, NeedsYou:
		return t.with(Idle{Outcome: Dismissed{}, At: d.Now}), change("task.dismiss", OutcomeCode(Dismissed{}), "", core.TaskDone)
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
	default:
		return t, nil
	}
	// The usage here is what the state holds; the shell replaces it with the
	// sum of the stored events before it commits.
	return toIdle(t, turn, att, u, "", Failed{Kind: FailInterrupted}, r.Now, false)
}
