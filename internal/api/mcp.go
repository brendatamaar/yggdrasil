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

// handleMCP is a stub until task M1. It never reaches a service: until the
// auth middleware exists, nothing is served.
func (s *Server) handleMCP(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The MCP server")
}

func (s *Server) handleMCPStatus(w http.ResponseWriter, _ *http.Request) {
	notReady(w, "The MCP status")
}
