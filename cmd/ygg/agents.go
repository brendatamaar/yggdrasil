package main

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
)

const (
	agentCheckTimeout = 3 * time.Second
	agentCacheTTL     = 10 * time.Second
)

// agentLabels are the names the composer shows. An adapter without an entry
// shows its ID.
var agentLabels = map[string]string{"claude-code": "Claude Code", "mock": "Demo agent"}

// agentService implements api.AgentService over the adapter registry. Each
// adapter's Check runs with a short timeout, and the answer is cached for a
// few seconds so a busy composer does not start the agent CLI on every
// keystroke.
type agentService struct {
	adapters adapterRegistry
	now      func() time.Time

	mu      sync.Mutex
	cached  []api.AgentDTO
	expires time.Time
}

var _ api.AgentService = (*agentService)(nil)

func newAgentService(adapters adapterRegistry) *agentService {
	return &agentService{adapters: adapters, now: time.Now}
}

// Agents lists the agents, sorted by ID.
func (s *agentService) Agents(ctx context.Context) ([]api.AgentDTO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.now().Before(s.expires) {
		return slices.Clone(s.cached), nil
	}
	ids := slices.Sorted(maps.Keys(s.adapters))
	out := make([]api.AgentDTO, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() { // ends when its Check returns or its 3 s timeout fires
			defer wg.Done()
			out[i] = s.describe(ctx, id)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.cached, s.expires = out, s.now().Add(agentCacheTTL)
	return slices.Clone(out), nil
}

func (s *agentService) describe(ctx context.Context, id string) api.AgentDTO {
	a := s.adapters[id]
	label := agentLabels[id]
	if label == "" {
		label = id
	}
	c := a.Capabilities()
	d := api.AgentDTO{ID: id, Label: label, Capabilities: api.AgentCapabilitiesDTO{
		Resume: c.Resume, CostReporting: c.CostReporting, Subagents: string(c.Subagents), Enforcement: string(c.Enforcement),
	}}
	cctx, cancel := context.WithTimeout(ctx, agentCheckTimeout)
	defer cancel()
	err := a.Check(cctx)
	if err == nil {
		d.Available = true
		return d
	}
	var um interface{ UserMessage() string }
	switch {
	case errors.As(err, &um) && um.UserMessage() != "":
		d.Reason = um.UserMessage()
	case errors.Is(err, context.DeadlineExceeded):
		d.Reason = "The agent did not answer in time. Check that it is installed and try again."
	default:
		// The raw error can hold paths or account data: never shown.
		d.Reason = "The agent is not ready. Check that it is installed and signed in."
	}
	return d
}
