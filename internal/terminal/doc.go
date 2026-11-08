// Package terminal is the shell of the terminal dock (DV-04, DV-05,
// docs/design/terminal.md). The decisions live in internal/core/terminal; this
// package parses outside data, calls Step, and runs the commands: the PTY
// through platform.StartPTY with the clean environment from internal/safety,
// the pump between PTY and socket, the expiry timer and the audit sink.
//
// API for internal/api (see Service for the details):
//
//	Issue(ctx, target) (token, expiresIn, err)    POST /api/terminal
//	Redeem(ctx, token, size) (*Session, err)      GET /ws/terminal, before the upgrade
//	Session.Run(ctx, socket) / Session.Abort()    after the upgrade / if it failed
//	Open(ctx, token, socket)                      Redeem + Run for a ready socket
//	CloseSession(id), Close(ctx)                  UI close button, shutdown
//
// Ports the caller supplies: Folders (tracked folders by ID), Socket (frames),
// AuditSink (terminal.open, terminal.close, terminal.open_failed with session
// ID, folder IDs, cause and exit code only), and safety.EnvBuilder.
//
// Origin: the handler checks Host and Origin before calling Redeem and
// rejects a bad request without touching the service, so a cross-site page
// cannot burn a user's token.
//
// The terminal is for the human only. It is not reachable from MCP or agent
// adapters; this package must not import internal/mcp or internal/agents (a
// test enforces it), and nothing in those packages may import this one.
package terminal
