package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// EventSink receives every event of a run, in order. The API layer persists
// and broadcasts them.
type EventSink interface {
	Append(ctx context.Context, taskID core.TaskID, e Event) error
}

// Result is how a run ended.
type Result struct {
	Status RunStatus
	// Usage is the sum of usage events, sub-agent usage included. A Cumulative
	// event replaces the total so far instead of adding to it (AG-21).
	Usage     Usage
	SessionID string
	Err       error
}

// Execute starts the adapter and forwards its events to the sink until the run
// ends. The returned error is set when the run could not start or the sink
// failed; an agent that fails on its own is reported in Result only.
// Everything runs in the caller's goroutine, so nothing outlives Execute.
// (Named Execute because the Run interface in adapter.go owns that name.)
func Execute(ctx context.Context, a Adapter, opts StartOptions, sink EventSink) (Result, error) {
	if opts.Env == nil {
		return Result{}, errors.New("run refused: no clean environment given")
	}
	if opts.ResumeSessionID != "" && !a.Capabilities().Resume {
		return Result{}, fmt.Errorf("run refused: %s cannot resume a session", a.Name())
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	run, err := a.Start(runCtx, opts)
	if err != nil {
		return Result{}, fmt.Errorf("start %s: %w", a.Name(), err)
	}

	var (
		res     Result
		seq     int64
		sinkErr error
	)
	// Late events (the final "cancelled" status) must still be stored.
	sinkCtx := context.WithoutCancel(ctx)
	for e := range run.Events() {
		if sinkErr != nil {
			continue // drain until the adapter closes the channel
		}
		seq++
		e.Seq = seq
		if e.At.IsZero() {
			e.At = time.Now()
		}
		if err := sink.Append(sinkCtx, opts.TaskID, e); err != nil {
			sinkErr = fmt.Errorf("store event %d: %w", seq, err)
			cancel()
			continue
		}
		switch e.Kind {
		case EventStatus:
			if e.Status != RunStarted {
				res.Status = e.Status
			}
		case EventUsage:
			if e.Usage != nil {
				addUsage(&res.Usage, *e.Usage)
			}
		}
	}
	waitErr := run.Wait()
	res.SessionID = run.SessionID()

	switch {
	case sinkErr != nil:
		res.Status, res.Err = RunFailed, sinkErr
		return res, sinkErr
	case res.Status == "" && ctx.Err() != nil:
		res.Status = RunCancelled
	case res.Status == "" && waitErr != nil:
		res.Status = RunFailed
	case res.Status == "":
		res.Status = RunCompleted
	}
	if res.Status != RunCancelled {
		res.Err = waitErr
	}
	return res, nil
}

func addUsage(total *Usage, u Usage) {
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

// Node is one event in a transcript tree.
type Node struct {
	Event    Event
	Children []*Node
	// Orphan: the event names a ParentID that no earlier event has as its ID.
	// It is attached to the root.
	Orphan bool
}

// BuildTree links events to the event whose ID is their ParentID and returns
// the roots, in input order. Events must be in Seq order: a parent is only
// found when it comes before its children, which also rules out cycles.
// When IDs repeat, the first event wins as parent.
func BuildTree(events []Event) []*Node {
	var roots []*Node
	byID := make(map[string]*Node, len(events))
	for _, e := range events {
		n := &Node{Event: e}
		if e.ParentID == "" {
			roots = append(roots, n)
		} else if p, ok := byID[e.ParentID]; ok {
			p.Children = append(p.Children, n)
		} else {
			n.Orphan = true
			roots = append(roots, n)
		}
		if e.ID != "" {
			if _, dup := byID[e.ID]; !dup {
				byID[e.ID] = n
			}
		}
	}
	return roots
}
