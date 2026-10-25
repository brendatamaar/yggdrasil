// Package proposals is the functional core of proposals (docs/design/
// proposals.md, RV-02): the lifecycle of one proposal (pending, confirmed,
// rejected, executed, failed), the closed set of actions, and the parsers that
// turn outside data into an Action. Nothing here does IO, reads the clock or
// logs; the shell in internal/proposals interprets the commands. The types
// carry no JSON tags: wire DTOs live in internal/api.
//
// There is no action that can express force, reset, clean, branch deletion or
// file deletion (invariant 1). Publish and subtasks stay in
// core.ProposalKind but ParseAction refuses them until their executors exist.
//
// Foundations only: Step, Hash, Describe and the action parsers are stubs
// until task X1. Step returns its proposal unchanged and no commands.
package proposals

import "errors"

// ErrNotImplemented is returned by boundary parsers that are still stubs.
var ErrNotImplemented = errors.New("proposals: not implemented yet")

// ErrUnsupportedKind is returned for a kind that exists in core.ProposalKind
// but cannot run yet (publish, subtasks), and for unknown kinds.
var ErrUnsupportedKind = errors.New("that kind of proposal isn't available")
