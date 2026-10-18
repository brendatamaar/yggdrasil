package builds

import (
	"slices"
	"strconv"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Step is the build state machine: (state2, commands) = Step(state1, input).
// An input that is illegal for the state returns the state unchanged and no
// commands (docs/design/builds.md, transition table).
func Step(s State, in Input) (State, []Command) {
	switch st := s.(type) {
	case None:
		if x, ok := in.(Enqueue); ok {
			return enqueue(x)
		}
	case Queued:
		switch x := in.(type) {
		case Admit:
			return admit(st, x)
		case Cancel:
			return cancelQueued(st, x)
		}
	case Running:
		return stepRunning(st, in)
	case Cancelling:
		return stepCancelling(st, in)
	case Finished:
		if x, ok := in.(ArtifactsCollected); ok {
			return collected(st, x)
		}
	}
	return s, nil
}

// Run feeds inputs to Step in order and returns the final state and every
// command emitted, in order. The property tests drive Step through it.
func Run(s State, inputs []Input) (State, []Command) {
	var all []Command
	for _, in := range inputs {
		var cmds []Command
		s, cmds = Step(s, in)
		all = append(all, cmds...)
	}
	return s, all
}

func audit(actor Actor, action string, id core.BuildID, detail map[string]string) WriteAudit {
	return WriteAudit{Actor: actor, Action: action, Subject: "build:" + string(id), Detail: detail}
}

func enqueue(x Enqueue) (State, []Command) {
	if x.ID.Validate() != nil || x.Actor.Validate() != nil || x.Spec.Validate() != nil {
		return None{}, nil
	}
	q := Queued{ID: x.ID, Spec: x.Spec, QueuedAt: x.Now, Actor: x.Actor}
	return q, []Command{
		audit(x.Actor, "build.queue", x.ID, map[string]string{"steps": strconv.Itoa(len(x.Spec.Steps))}),
		Publish{ID: x.ID}, Wake{},
	}
}

func startCmd(id core.BuildID, spec Spec, i int) StartStep {
	st := spec.Steps[i]
	return StartStep{BuildID: id, Index: i, Attempt: 1, RepoID: st.RepoID, Target: spec.Target, Argv: slices.Clone(st.Argv)}
}

func admit(q Queued, x Admit) (State, []Command) {
	if len(q.Spec.Steps) == 0 {
		return q, nil
	}
	steps := make([]StepState, len(q.Spec.Steps))
	for i := range steps {
		steps[i] = StepPending{}
	}
	steps[0] = StepRunning{Attempt: 1, StartedAt: x.Now}
	r := Running{ID: q.ID, Spec: q.Spec, QueuedAt: q.QueuedAt, StartedAt: x.Now, Actor: q.Actor, Steps: steps}
	return r, []Command{audit(q.Actor, "build.start", q.ID, nil), startCmd(q.ID, q.Spec, 0), Publish{ID: q.ID}}
}

func skipped(n int) []StepState {
	s := make([]StepState, n)
	for i := range s {
		s[i] = StepSkipped{}
	}
	return s
}

func cancelQueued(q Queued, x Cancel) (State, []Command) {
	at := x.Now
	f := Finished{
		ID: q.ID, Spec: q.Spec, QueuedAt: q.QueuedAt, Actor: q.Actor,
		CancelledAt: &at, FinishedAt: x.Now, Steps: skipped(len(q.Spec.Steps)), Result: ResultCancelled{},
	}
	return f, []Command{audit(x.Actor, "build.cancel", q.ID, nil), Publish{ID: q.ID}, Wake{}}
}

// live returns the running step when (index, attempt) names it.
func live(r Running, index, attempt int) (StepRunning, bool) {
	if index != r.Current || index < 0 || index >= len(r.Steps) {
		return StepRunning{}, false
	}
	rs, ok := r.Steps[index].(StepRunning)
	return rs, ok && rs.Attempt == attempt
}

// finish ends r at the current step with the given outcome and result.
func finish(r Running, cancelledAt *time.Time, o Outcome, trunc bool, res Result, now time.Time) Finished {
	rs := r.Steps[r.Current].(StepRunning)
	steps := slices.Clone(r.Steps)
	steps[r.Current] = StepDone{Attempt: rs.Attempt, StartedAt: rs.StartedAt, FinishedAt: now, Outcome: o, LogTruncated: trunc}
	for i := r.Current + 1; i < len(steps); i++ {
		steps[i] = StepSkipped{}
	}
	started := r.StartedAt
	return Finished{
		ID: r.ID, Spec: r.Spec, QueuedAt: r.QueuedAt, Actor: r.Actor, StartedAt: &started,
		CancelledAt: cancelledAt, FinishedAt: now, Steps: steps, Result: res,
	}
}

func repoIDs(spec Spec) []core.RepoID {
	var ids []core.RepoID
	for _, st := range spec.Steps {
		if !slices.Contains(ids, st.RepoID) {
			ids = append(ids, st.RepoID)
		}
	}
	return ids
}

func stepRunning(r Running, in Input) (State, []Command) {
	switch x := in.(type) {
	case StepExited:
		rs, ok := live(r, x.Index, x.Attempt)
		if !ok {
			return r, nil
		}
		i := r.Current
		if x.ExitCode != 0 {
			f := finish(r, nil, Failed{ExitCode: x.ExitCode}, x.LogTruncated, ResultFailed{Step: i}, x.Now)
			return f, failedCmds(r, i)
		}
		steps := slices.Clone(r.Steps)
		steps[i] = StepDone{Attempt: rs.Attempt, StartedAt: rs.StartedAt, FinishedAt: x.Now, Outcome: Succeeded{}, LogTruncated: x.LogTruncated}
		if i+1 < len(steps) {
			steps[i+1] = StepRunning{Attempt: 1, StartedAt: x.Now}
			n := r
			n.Steps, n.Current = steps, i+1
			return n, []Command{
				audit(r.Actor, "build.step", r.ID, map[string]string{"step": strconv.Itoa(i), "outcome": "succeeded"}),
				startCmd(r.ID, r.Spec, i+1), Publish{ID: r.ID},
			}
		}
		started := r.StartedAt
		f := Finished{ID: r.ID, Spec: r.Spec, QueuedAt: r.QueuedAt, Actor: r.Actor, StartedAt: &started,
			FinishedAt: x.Now, Steps: steps, Result: ResultSucceeded{}}
		return f, []Command{
			audit(r.Actor, "build.finish", r.ID, map[string]string{"result": "succeeded"}),
			CollectArtifacts{BuildID: r.ID, Target: r.Spec.Target, RepoIDs: repoIDs(r.Spec)}, Publish{ID: r.ID}, Wake{},
		}
	case StepStartFailed:
		if _, ok := live(r, x.Index, x.Attempt); !ok || x.Reason.Validate() != nil {
			return r, nil
		}
		f := finish(r, nil, StartFailed{Reason: x.Reason}, false, ResultFailed{Step: r.Current}, x.Now)
		return f, failedCmds(r, r.Current)
	case Cancel:
		rs := r.Steps[r.Current].(StepRunning)
		c := Cancelling{Running: r, CancelledAt: x.Now}
		return c, []Command{
			audit(x.Actor, "build.cancel", r.ID, nil),
			KillStep{BuildID: r.ID, Index: r.Current, Attempt: rs.Attempt}, Publish{ID: r.ID},
		}
	case ProcessLost:
		f := finish(r, nil, Interrupted{}, false, ResultInterrupted{Step: r.Current}, x.Now)
		return f, []Command{
			audit("system", "build.interrupted", r.ID, map[string]string{"step": strconv.Itoa(r.Current)}),
			Publish{ID: r.ID}, Wake{},
		}
	}
	return r, nil
}

func failedCmds(r Running, step int) []Command {
	return []Command{
		audit(r.Actor, "build.finish", r.ID, map[string]string{"result": "failed", "step": strconv.Itoa(step)}),
		Publish{ID: r.ID}, Wake{},
	}
}

func stepCancelling(c Cancelling, in Input) (State, []Command) {
	var now time.Time
	trunc := false
	switch x := in.(type) {
	case StepExited:
		if _, ok := live(c.Running, x.Index, x.Attempt); !ok {
			return c, nil
		}
		now, trunc = x.Now, x.LogTruncated
	case StepStartFailed:
		if _, ok := live(c.Running, x.Index, x.Attempt); !ok || x.Reason.Validate() != nil {
			return c, nil
		}
		now = x.Now
	case ProcessLost:
		now = x.Now
	default:
		return c, nil
	}
	at := c.CancelledAt
	f := finish(c.Running, &at, Cancelled{}, trunc, ResultCancelled{}, now)
	return f, []Command{
		audit(c.Actor, "build.finish", c.ID, map[string]string{"result": "cancelled"}),
		Publish{ID: c.ID}, Wake{},
	}
}

func collected(f Finished, x ArtifactsCollected) (State, []Command) {
	if _, ok := f.Result.(ResultSucceeded); !ok || f.Artifacts != nil {
		return f, nil
	}
	arts := slices.Clone(x.Artifacts)
	if arts == nil {
		arts = []Artifact{}
	}
	f.Artifacts = arts
	return f, []Command{
		audit(f.Actor, "build.artifacts", f.ID, map[string]string{"count": strconv.Itoa(len(arts))}),
		Publish{ID: f.ID},
	}
}
