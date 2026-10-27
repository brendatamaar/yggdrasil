package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
)

type userErr struct{}

func (userErr) Error() string       { return "raw: C:\\secret\\claude.exe failed" }
func (userErr) UserMessage() string { return "Install Claude Code, then try again." }

// checkAdapter wraps a real adapter with a controllable Check.
type checkAdapter struct {
	agents.Adapter
	err   error
	calls atomic.Int32
	block bool
}

func (c *checkAdapter) Check(ctx context.Context) error {
	c.calls.Add(1)
	if c.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.err
}

func TestAgentServiceReportsAvailabilityAndReasons(t *testing.T) {
	m := mock.New(mock.Options{Fixture: "subagents"})
	reg := adapterRegistry{
		"mock":   m,
		"broken": &checkAdapter{Adapter: m, err: userErr{}},
		"opaque": &checkAdapter{Adapter: m, err: errors.New("exec: C:\\secret: not found")},
	}
	s := newAgentService(reg)
	list, err := s.Agents(context.Background())
	if err != nil || len(list) != 3 {
		t.Fatalf("%v %+v", err, list)
	}
	by := map[string]string{}
	avail := map[string]bool{}
	for _, a := range list {
		by[a.ID], avail[a.ID] = a.Reason, a.Available
	}
	if !avail["mock"] || by["mock"] != "" || avail["broken"] || by["broken"] != "Install Claude Code, then try again." {
		t.Errorf("list = %+v", list)
	}
	if avail["opaque"] || by["opaque"] == "" || by["opaque"] == "exec: C:\\secret: not found" {
		t.Errorf("opaque reason = %q", by["opaque"])
	}
	if list[0].ID != "broken" || list[0].Capabilities.Subagents == "" {
		t.Errorf("not sorted or capabilities missing: %+v", list[0])
	}
}

func TestAgentServiceCachesForTenSeconds(t *testing.T) {
	ca := &checkAdapter{Adapter: mock.New(mock.Options{Fixture: "subagents"})}
	s := newAgentService(adapterRegistry{"a": ca})
	now := time.Now()
	s.now = func() time.Time { return now }
	for range 3 {
		if _, err := s.Agents(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if ca.calls.Load() != 1 {
		t.Fatalf("Check ran %d times within the cache window", ca.calls.Load())
	}
	now = now.Add(agentCacheTTL + time.Second)
	_, _ = s.Agents(context.Background())
	if ca.calls.Load() != 2 {
		t.Fatalf("Check ran %d times after the cache expired", ca.calls.Load())
	}
}

func TestAgentServiceTimesOutASlowCheck(t *testing.T) {
	ca := &checkAdapter{Adapter: mock.New(mock.Options{Fixture: "subagents"}), block: true}
	s := newAgentService(adapterRegistry{"slow": ca})
	start := time.Now()
	list, err := s.Agents(context.Background())
	if err != nil || len(list) != 1 || list[0].Available || list[0].Reason == "" {
		t.Fatalf("%v %+v", err, list)
	}
	if d := time.Since(start); d > agentCheckTimeout+2*time.Second {
		t.Errorf("took %v", d)
	}
}
