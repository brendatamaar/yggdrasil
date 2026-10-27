package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

type fakeAgents struct {
	list []AgentDTO
	err  error
}

func (f fakeAgents) Agents(context.Context) ([]AgentDTO, error) { return f.list, f.err }

func TestAgentsListIsServed(t *testing.T) {
	want := []AgentDTO{{ID: "mock", Label: "Demo agent", Available: true, Capabilities: AgentCapabilitiesDTO{Subagents: "nested", Enforcement: "hook"}},
		{ID: "claude-code", Label: "Claude Code", Reason: "Install it first."}}
	e := newEnv(t, func(d *Deps) { d.Agents = fakeAgents{list: want} })
	resp, data := e.do(t, http.MethodGet, "/api/agents", nil)
	var got AgentsResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &got) != nil || len(got.Agents) != 2 || got.Agents[0] != want[0] || got.Agents[1] != want[1] {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
}

func TestAgentsListErrorIsGeneric(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Agents = fakeAgents{err: errors.New("boom at C:\\secret")} })
	resp, data := e.do(t, http.MethodGet, "/api/agents", nil)
	if resp.StatusCode != 500 || json.Valid(data) == false {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
}

func TestAgentsAreNotReadyWithoutAService(t *testing.T) {
	e := newEnv(t)
	if resp, _ := e.do(t, http.MethodGet, "/api/agents", nil); resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHealthReportsWriteModes(t *testing.T) {
	for _, on := range []bool{false, true} {
		e := newEnv(t, func(d *Deps) { d.WriteModes = on })
		resp, data := e.do(t, http.MethodGet, "/api/health", nil)
		var h HealthResponse
		if resp.StatusCode != 200 || json.Unmarshal(data, &h) != nil || h.WriteModes != on || h.Status != "ok" {
			t.Fatalf("writeModes=%v: %d %s", on, resp.StatusCode, data)
		}
	}
}
