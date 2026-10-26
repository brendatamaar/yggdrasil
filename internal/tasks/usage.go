package tasks

import (
	"encoding/json"
	"math"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// toUsage converts what an adapter reported into the core's exact units. A
// missing cost is "unknown", never zero.
func toUsage(u agents.Usage) coretasks.Usage {
	out := coretasks.Usage{
		InputTokens: nonNeg(u.InputTokens), OutputTokens: nonNeg(u.OutputTokens),
		CacheWriteTokens: nonNeg(u.CacheWriteTokens), CacheReadTokens: nonNeg(u.CacheReadTokens),
		DurationMS: nonNeg(u.DurationMS),
	}
	if u.CostUSD != nil && *u.CostUSD >= 0 && !math.IsNaN(*u.CostUSD) && !math.IsInf(*u.CostUSD, 0) && *u.CostUSD < 1e9 {
		out.CostMicroUSD = int64(math.Round(*u.CostUSD * 1e6))
		out.CostKnown = true
	}
	return out
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// SumUsage recomputes the usage of the last run of a task from its stored
// events, the way agents.Execute adds it: usage events add up, a cumulative
// event replaces the total so far. Events before the last "started" status
// belong to earlier turns and are left out. An event that does not decode is
// skipped and counted.
func SumUsage(events []db.TaskEvent) (u coretasks.Usage, skipped int) {
	decoded := make([]agents.Event, 0, len(events))
	for _, row := range events {
		var e agents.Event
		if err := json.Unmarshal(row.Data, &e); err != nil {
			skipped++
			continue
		}
		decoded = append(decoded, e)
	}
	start := 0
	for i, e := range decoded {
		if e.Kind == agents.EventStatus && e.Status == agents.RunStarted {
			start = i
		}
	}
	var total agents.Usage
	for _, e := range decoded[start:] {
		if e.Kind != agents.EventUsage || e.Usage == nil {
			continue
		}
		addUsage(&total, *e.Usage)
	}
	return toUsage(total), skipped
}

// addUsage mirrors agents.Execute's accounting of usage events.
func addUsage(total *agents.Usage, u agents.Usage) {
	if u.Cumulative {
		cost := u.CostUSD
		*total = u
		total.Cumulative = false
		if cost != nil {
			c := *cost
			total.CostUSD = &c
		}
		return
	}
	total.InputTokens += u.InputTokens
	total.OutputTokens += u.OutputTokens
	total.CacheWriteTokens += u.CacheWriteTokens
	total.CacheReadTokens += u.CacheReadTokens
	total.DurationMS += u.DurationMS
	total.Turns += u.Turns
	if u.CostUSD != nil {
		c := *u.CostUSD
		if total.CostUSD != nil {
			c += *total.CostUSD
		}
		total.CostUSD = &c
	}
}

// batcher collects the usage and sub-agent inputs of a run and feeds them to
// the core at most once per every (docs/design/tasks.md call graph). A
// sub-agent start flushes at once: it can exceed a cap and must not wait.
// It is used by the one goroutine that runs the agent, so it needs no lock.
type batcher struct {
	s       *Service
	id      core.TaskID
	turn    coretasks.TurnID
	attempt coretasks.Attempt
	pending []coretasks.Input
	last    time.Time
}

func newBatcher(s *Service, id core.TaskID, turn coretasks.TurnID, a coretasks.Attempt) *batcher {
	return &batcher{s: s, id: id, turn: turn, attempt: a, last: s.c.Now()}
}

func (b *batcher) usage(u coretasks.Usage, cumulative bool) {
	if n := len(b.pending); n > 0 {
		if prev, ok := b.pending[n-1].(coretasks.UsageReported); ok && prev.Cumulative == cumulative {
			if cumulative {
				prev.Usage = u
			} else {
				prev.Usage = addCoreUsage(prev.Usage, u)
			}
			b.pending[n-1] = prev
			b.maybeFlush(false)
			return
		}
	}
	b.pending = append(b.pending, coretasks.UsageReported{Turn: b.turn, Attempt: b.attempt, Usage: u, Cumulative: cumulative})
	b.maybeFlush(false)
}

func (b *batcher) subagentStarted(agentID string, depth int) {
	b.pending = append(b.pending, coretasks.SubagentStarted{Turn: b.turn, Attempt: b.attempt, AgentID: agentID, Depth: depth})
	b.maybeFlush(true)
}

func (b *batcher) subagentStopped(agentID string) {
	b.pending = append(b.pending, coretasks.SubagentStopped{Turn: b.turn, Attempt: b.attempt, AgentID: agentID})
	b.maybeFlush(false)
}

func (b *batcher) maybeFlush(force bool) {
	if force || b.s.c.Now().Sub(b.last) >= b.s.c.UsageEvery {
		b.flush()
	}
}

// flush feeds everything collected, in order.
func (b *batcher) flush() {
	pending := b.pending
	b.pending = nil
	b.last = b.s.c.Now()
	for _, in := range pending {
		b.s.feed(b.id, in, applyOpts{})
	}
}

func addCoreUsage(a, b coretasks.Usage) coretasks.Usage {
	return coretasks.Usage{
		InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens, CacheReadTokens: a.CacheReadTokens + b.CacheReadTokens,
		CostMicroUSD: a.CostMicroUSD + b.CostMicroUSD, CostKnown: a.CostKnown || b.CostKnown,
		DurationMS: a.DurationMS + b.DurationMS,
	}
}
