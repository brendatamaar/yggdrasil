// Package mcp is the functional core of the MCP server (docs/design/mcp.md,
// MCP-01 to MCP-06): the closed tool set, which tools a task is offered, how a
// tool call is parsed into a typed value, and the call-rate limiter. Nothing
// here does IO, reads the clock or logs; the shell in internal/mcp does the
// work through the existing services. The types carry no JSON tags.
//
// There is no tool that confirms, rejects or executes a proposal (M4), and
// propose_publish and propose_subtasks are not in the set until their
// executors exist.
//
// Foundations only: Offered and Authorize fail closed (nothing offered,
// everything denied) and ParseCall is a stub until task M1. The limiter is
// implemented.
package mcp

import "errors"

// ErrNotImplemented is returned by boundary parsers that are still stubs.
var ErrNotImplemented = errors.New("mcp: not implemented yet")
