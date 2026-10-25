package api

// DTOs of the MCP server (MCP-01 to MCP-06, docs/design/mcp.md). The /mcp
// endpoint speaks the MCP protocol, so it has no REST DTOs of its own; this is
// the status the UI can show about it.

// McpStatusDTO is the body of GET /api/mcp/status. Url is the loopback
// address agents are given; it never contains a token.
type McpStatusDTO struct {
	Enabled bool   `json:"enabled"`
	Url     string `json:"url"`
	// ActiveSessions is the number of tasks with a live MCP session.
	ActiveSessions int `json:"activeSessions"`
}
