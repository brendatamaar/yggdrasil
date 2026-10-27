package api

import (
	"context"
	"net/http"
)

// This file is Go-only wiring and is excluded from tygo (tygo.yaml). The DTOs
// are in types_agents.go.

// AgentService lists the registered agents with their availability. cmd/ygg
// implements it over the adapter registry; Agents may cache for a few
// seconds. Errors are classified by internal/apperr.
type AgentService interface {
	Agents(ctx context.Context) ([]AgentDTO, error)
}

func (s *Server) routesAgents(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", s.handleListAgents)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		notReady(w, "The agent list")
		return
	}
	list, err := s.agents.Agents(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if list == nil {
		list = []AgentDTO{}
	}
	writeJSON(w, http.StatusOK, AgentsResponse{Agents: list})
}
