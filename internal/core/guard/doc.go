// Package guard is the functional core of what an agent may do
// (docs/design/guard.md): the path and command decision for one tool call, the
// admission of a mode for a turn, the Ask-mode integrity check and the trust
// of a repo's own agent config. Nothing here does IO, reads the clock or
// logs; the shell (internal/safety, the adapters, internal/tasks) parses
// outside data into these types and interprets the commands. The types carry
// no JSON tags.
//
// Foundations only: every decision is a fail-closed stub until task G1.
// Decide denies, Admit admits Ask only, and the Step functions change nothing.
package guard

import "errors"

// ErrNotImplemented is returned by boundary parsers that are still stubs.
var ErrNotImplemented = errors.New("guard: not implemented yet")
