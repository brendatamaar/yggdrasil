package api

import (
	"context"
	"net/http"
)

// This file is owned by the MCP server task (M1; MCP-01 to MCP-06). The DTO is
// in types_mcp.go. The file is excluded from tygo (tygo.yaml): it holds
// Go-only wiring.

// MCPService is implemented by internal/mcp (docs/design/mcp.md). It serves
// the MCP protocol on /mcp (streamable HTTP) and checks the per-task bearer
// token itself. The api package adds the Host check, refuses any request that
// carries an Origin header (an MCP client is not a browser) and caps the body
// at 1 MiB; those rules are part of task M1.
type MCPService interface {
	// Handler serves /mcp.
	Handler() http.Handler
	// Status says whether the server is on and how many sessions are live.
	Status(ctx context.Context) McpStatusDTO
}

// routesMCP registers /mcp and its status endpoint.
func (s *Server) routesMCP(mux *http.ServeMux) {
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("GET /api/mcp/status", s.handleMCPStatus)
}

// maxMCPBody is the largest request body /mcp accepts.
const maxMCPBody = 1 << 20

// handleMCP serves the MCP protocol. The Host check ran in the guard
// middleware; here a request that carries any Origin header is refused (an MCP
// client is not a browser, and a page must never reach this endpoint) and the
// body is capped. The service checks the per-task bearer token itself and
// answers 401 with an empty body when it is missing or wrong.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		notReady(w, "The MCP server")
		return
	}
	if r.Header.Get("Origin") != "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMCPBody)
	s.mcp.Handler().ServeHTTP(w, r)
}

func (s *Server) handleMCPStatus(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		notReady(w, "The MCP status")
		return
	}
	writeJSON(w, http.StatusOK, s.mcp.Status(r.Context()))
}
