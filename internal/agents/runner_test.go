package agents_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

type memSink struct {
	mu     sync.Mutex
	events []agents.Event
	failAt int // fail the Nth Append (1-based); 0 never
	onEach func()
}

func (s *memSink) Append(_ context.Context, _ core.TaskID, e agents.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	if s.failAt > 0 && len(s.events) == s.failAt {
		return errors.New("disk full")
	}
	if s.onEach != nil {
		s.onEach()
	}
	return nil
}

func opts() agents.StartOptions {
	return agents.StartOptions{TaskID: "t_1", Env: []string{}}
}

func TestRunSeqAndUsage(t *testing.T) {
	s := &memSink{}
	res, err := agents.Execute(context.Background(), mock.New(mock.Options{Fixture: "subagents"}), opts(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != agents.RunCompleted || res.Err != nil {
		t.Fatalf("result = %+v", res)
	}
	for i, e := range s.events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has Seq %d", i, e.Seq)
		}
		if e.At.IsZero() {
			t.Fatalf("event %d has no At", i)
		}
	}
	// 500+100+60 in, 80+20+10 out: sub-agent usage counts.
	if res.Usage.InputTokens != 660 || res.Usage.OutputTokens != 110 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.Usage.CostUSD == nil || *res.Usage.CostUSD < 0.0114 || *res.Usage.CostUSD > 0.0116 {
		t.Errorf("cost = %v", res.Usage.CostUSD)
	}
	if res.SessionID != "mock-subagents" {
		t.Errorf("session = %q", res.SessionID)
	}
}

func TestRunParentLinks(t *testing.T) {
	s := &memSink{}
	if _, err := agents.Execute(context.Background(), mock.New(mock.Options{Fixture: "subagents"}), opts(), s); err != nil {
		t.Fatal(err)
	}
	roots := agents.BuildTree(s.events)
	var sa1 *agents.Node
	for _, r := range roots {
		if r.Event.ID == "sa_1" {
			sa1 = r
		}
		if r.Orphan {
			t.Errorf("orphan root: %+v", r.Event)
		}
	}
	if sa1 == nil {
		t.Fatal("sa_1 not a root")
	}
	var sa2 *agents.Node
	for _, c := range sa1.Children {
		if c.Event.ID == "sa_2" {
			sa2 = c
			continue
		}
		if c.Event.AgentID != "explorer" {
			t.Errorf("child of sa_1 has agent %q", c.Event.AgentID)
		}
	}
	if sa2 == nil {
		t.Fatal("sa_2 is not a child of sa_1")
	}
	if len(sa2.Children) != 3 { // message, usage, end
		t.Errorf("sa_2 has %d children", len(sa2.Children))
	}
}

func TestBuildTreeOrphan(t *testing.T) {
	roots := agents.BuildTree([]agents.Event{
		{Seq: 1, ID: "a"},
		{Seq: 2, ID: "b", ParentID: "ghost"},
		{Seq: 3, ParentID: "a"},
	})
	if len(roots) != 2 || !roots[1].Orphan || roots[0].Orphan || len(roots[0].Children) != 1 {
		t.Fatalf("roots = %+v", roots)
	}
}

func TestRunCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &memSink{onEach: func() {}}
	n := 0
	s.onEach = func() {
		if n++; n == 2 {
			cancel()
		}
	}
	res, err := agents.Execute(ctx, mock.New(mock.Options{Fixture: "plain", Delay: time.Millisecond}), opts(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != agents.RunCancelled {
		t.Fatalf("status = %q", res.Status)
	}
	last := s.events[len(s.events)-1]
	if last.Kind != agents.EventStatus || last.Status != agents.RunCancelled {
		t.Errorf("last event = %+v", last)
	}
	if len(s.events) >= 7 {
		t.Errorf("replay did not stop: %d events", len(s.events))
	}
}

func TestRunSinkError(t *testing.T) {
	s := &memSink{failAt: 3}
	res, err := agents.Execute(context.Background(), mock.New(mock.Options{Fixture: "plain"}), opts(), s)
	if err == nil || res.Status != agents.RunFailed || res.Err == nil {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(s.events) != 3 {
		t.Errorf("sink got %d events after failing", len(s.events))
	}
}

func TestRunAgentFailure(t *testing.T) {
	res, err := agents.Execute(context.Background(), mock.New(mock.Options{Fixture: "error"}), opts(), &memSink{})
	if err != nil || res.Status != agents.RunFailed || res.Err == nil {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

type noResume struct{ *mock.Adapter }

func (noResume) Capabilities() agents.Capabilities { return agents.Capabilities{} }

func TestRunRefusals(t *testing.T) {
	o := opts()
	o.ResumeSessionID = "s1"
	if _, err := agents.Execute(context.Background(), noResume{mock.New(mock.Options{})}, o, &memSink{}); err == nil {
		t.Error("resume without capability was accepted")
	}
	o = opts()
	o.Env = nil
	if _, err := agents.Execute(context.Background(), mock.New(mock.Options{}), o, &memSink{}); err == nil {
		t.Error("nil Env was accepted")
	}
}

func TestRunResume(t *testing.T) {
	o := opts()
	o.ResumeSessionID = "s1"
	res, err := agents.Execute(context.Background(), mock.New(mock.Options{}), o, &memSink{})
	if err != nil || res.SessionID != "mock-resumed-s1" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestCumulativeUsageReplaces(t *testing.T) {
	s := &memSink{}
	a := stubAdapter{events: []agents.Event{
		{Kind: agents.EventUsage, Usage: &agents.Usage{InputTokens: 10}},
		{Kind: agents.EventUsage, Usage: &agents.Usage{InputTokens: 50, Cumulative: true}},
		{Kind: agents.EventUsage, Usage: &agents.Usage{InputTokens: 5}},
	}}
	res, err := agents.Execute(context.Background(), a, opts(), s)
	if err != nil || res.Usage.InputTokens != 55 || res.Status != agents.RunCompleted {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

type stubAdapter struct{ events []agents.Event }

func (stubAdapter) Name() string                      { return "stub" }
func (stubAdapter) Capabilities() agents.Capabilities { return agents.Capabilities{} }
func (stubAdapter) Check(context.Context) error       { return nil }
func (a stubAdapter) Start(context.Context, agents.StartOptions) (agents.Run, error) {
	ch := make(chan agents.Event, len(a.events))
	for _, e := range a.events {
		ch <- e
	}
	close(ch)
	return stubRun{ch}, nil
}

type stubRun struct{ ch chan agents.Event }

func (r stubRun) Events() <-chan agents.Event { return r.ch }
func (stubRun) SessionID() string             { return "" }
func (stubRun) Wait() error                   { return nil }

func TestNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		s := &memSink{failAt: 2}
		_, _ = agents.Execute(ctx, mock.New(mock.Options{Fixture: "subagents", Delay: time.Millisecond}), opts(), s)
		cancel()
		_, _ = agents.Execute(context.Background(), mock.New(mock.Options{Fixture: "edits"}), opts(), &memSink{})
	}
	for i := 0; i < 50 && runtime.NumGoroutine() > before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines: %d before, %d after", before, after)
	}
}
