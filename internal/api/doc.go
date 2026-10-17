// Package api holds the HTTP handlers, WebSocket hubs, Host/Origin checks and
// remote-mode auth.
//
// Handlers are boundary code (docs/AGENTS.md 5.11). A handler:
//
//  1. parses the request (body, path and query values) into core types and
//     rejects invalid values with a 400 that names the rule;
//  2. calls a narrow interface defined in this package (BuildService,
//     RepoService, ...) that the owning package implements. The owning
//     package loads the state, calls the pure step, commits the state and
//     its audit entries in one transaction, and runs the commands;
//  3. maps the error class the service returns to an HTTP status and a
//     plain-language message.
//
// New handlers don't take *db.Store and don't decide domain rules themselves.
// The task handlers in tasks.go predate this rule and move to core/tasks in
// Phase 3.
package api
