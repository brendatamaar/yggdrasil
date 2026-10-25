package tasks

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const propRuns = 400

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// forSeeds runs f with a deterministic generator per seed and names the seed
// of a failing run.
func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		ok := t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) })
		if !ok {
			return
		}
	}
}

// StepRecord is one step of a run.
type StepRecord struct {
	Prev Task
	In   Input
	Next Task
	Cmds []Command
}

// Run feeds inputs to Step one after the other and records every step.
func Run(initial Task, inputs []Input) []StepRecord {
	out := make([]StepRecord, 0, len(inputs))
	cur := initial
	for _, in := range inputs {
		next, cmds := Step(cur, in)
		out = append(out, StepRecord{Prev: cur, In: in, Next: next, Cmds: cmds})
		cur = next
	}
	return out
}

func liveState(s State) bool {
	switch s.(type) {
	case Waiting, Starting, Running, Stopping:
		return true
	}
	return false
}

func activeState(s State) bool {
	switch s.(type) {
	case Starting, Running, Stopping:
		return true
	}
	return false
}

func turnOf(s State) TurnID {
	switch st := s.(type) {
	case Waiting:
		return st.Turn
	case Starting:
		return st.Turn
	case Running:
		return st.Turn
	case Stopping:
		return st.Turn
	}
	return ""
}

func attemptOf(s State) Attempt {
	switch st := s.(type) {
	case Waiting:
		return st.Attempt
	case Starting:
		return st.Attempt
	case Running:
		return st.Attempt
	case Stopping:
		return st.Attempt
	}
	return 0
}

func usageOf(s State) Usage {
	switch st := s.(type) {
	case Running:
		return st.Usage
	case Stopping:
		return st.Usage
	}
	return Usage{}
}

func usageGE(a, b Usage) bool {
	return a.InputTokens >= b.InputTokens && a.OutputTokens >= b.OutputTokens &&
		a.CacheWriteTokens >= b.CacheWriteTokens && a.CacheReadTokens >= b.CacheReadTokens &&
		a.CostMicroUSD >= b.CostMicroUSD && a.DurationMS >= b.DurationMS
}

func genUsage(r *rand.Rand) Usage {
	u := Usage{InputTokens: int64(r.Intn(50) - 5), OutputTokens: int64(r.Intn(50)), DurationMS: int64(r.Intn(20))}
	if r.Intn(2) == 0 {
		u.CostKnown = true
		u.CostMicroUSD = int64(r.Intn(100))
	}
	return u
}

var allModes = []core.TaskMode{core.ModeAsk, core.ModeCode, core.ModeCurrentBranch, core.ModeProject}

// genInput mixes legal, stale and duplicate inputs, biased by the state.
func genInput(r *rand.Rand, t Task, now time.Time, turnSeq *int) Input {
	turn, att := turnOf(t.State), attemptOf(t.State)
	if turn == "" || r.Intn(8) == 0 {
		turn = TurnID(fmt.Sprintf("t%d", 1+r.Intn(*turnSeq+1)))
	}
	if r.Intn(8) == 0 {
		att += Attempt(r.Intn(3) - 1)
	}
	switch r.Intn(16) {
	case 0, 1:
		*turnSeq++
		m := Message{
			Turn: TurnID(fmt.Sprintf("t%d", *turnSeq)), Mode: allModes[r.Intn(4)], Prompt: "do it", Now: now,
			Admission: Admission{Admitted: r.Intn(5) > 0, Reason: "r", Message: "no"},
			Caps:      Caps{Depth: r.Intn(3), Concurrent: r.Intn(3)},
		}
		if m.Mode == core.ModeCurrentBranch || r.Intn(6) == 0 {
			m.EditsMain = []core.RepoID{"r1"}
		}
		return m
	case 2, 3:
		return Admitted{Turn: turn, Now: now}
	case 4, 5:
		return Prepared{Turn: turn, Attempt: att, Dir: "w", ExtraDirs: []string{"x"}, Now: now}
	case 6:
		if r.Intn(2) == 0 {
			return PrepareFailed{Turn: turn, Attempt: att, Reason: "git", Now: now}
		}
		return LocksDenied{Turn: turn, Attempt: att, Repo: "r1", Holder: "build"}
	case 7, 8:
		return UsageReported{Attempt: att, Usage: genUsage(r), Cumulative: r.Intn(2) == 0}
	case 9, 10:
		id := fmt.Sprintf("a%d", r.Intn(3))
		if r.Intn(3) == 0 {
			return SubagentStopped{Attempt: att, AgentID: id}
		}
		return SubagentStarted{Attempt: att, AgentID: id, Depth: 1 + r.Intn(3)}
	case 11, 12:
		st := []RunStatus{RunCompleted, RunFailed, RunCancelled}[r.Intn(3)]
		return RunFinished{Turn: turn, Attempt: att, Status: st, StartFail: r.Intn(4) == 0, Usage: genUsage(r), SessionID: []string{"", "s1"}[r.Intn(2)], Now: now}
	case 13:
		if r.Intn(2) == 0 {
			return CancelRequested{Now: now}
		}
		return IntegrityViolated{Turn: turn, Now: now}
	case 14:
		if r.Intn(2) == 0 {
			return DismissRequested{Now: now}
		}
		return ProcessesGone{Turn: turn, Attempt: att}
	default:
		if r.Intn(3) == 0 {
			return Recovered{Now: now}
		}
		return UsageReported{Attempt: att, Usage: genUsage(r)}
	}
}

type checker func(prev Task, in Input, next Task, cmds []Command) error

// drive runs random sequences from Draft or a finished task and calls a fresh
// checker per run after every step. Input times never go backwards.
func drive(t *testing.T, mk func() checker) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		chk := mk()
		var task Task
		if r.Intn(2) == 0 {
			task = Task{ID: "task1", State: Draft{}}
		} else {
			task = Task{ID: "task1", Mode: core.ModeAsk, Turns: 1, State: Idle{Outcome: Answered{}, At: epoch}}
		}
		now := epoch
		turnSeq := 0
		for i := 0; i < 60; i++ {
			now = now.Add(time.Duration(r.Intn(3)) * time.Second)
			in := genInput(r, task, now, &turnSeq)
			next, cmds := Step(task, in)
			if err := chk(task, in, next, cmds); err != nil {
				t.Fatalf("step %d, input %#v\nprev  %#v\nnext  %#v\ncmds  %#v\n%v", i, in, task, next, cmds, err)
			}
			task = next
		}
	})
}

func hasCmd[C Command](cmds []Command) bool {
	for _, c := range cmds {
		if _, ok := c.(C); ok {
			return true
		}
	}
	return false
}

func publishes(cmds []Command, s core.TaskStatus) bool {
	for _, c := range cmds {
		if p, ok := c.(Publish); ok && p.Status == s {
			return true
		}
	}
	return false
}

func sameTask(a, b Task) bool { return fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b) }
