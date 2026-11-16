// Package proposals is the functional core of proposals (docs/design/
// proposals.md, RV-02): the lifecycle of one proposal (pending, confirmed,
// rejected, executed, failed), the closed set of actions, and the parsers that
// turn outside data into an Action. Nothing here does IO, reads the clock or
// logs; the shell in internal/proposals interprets the commands. The types
// carry no JSON tags: wire DTOs live in internal/api.
//
// There is no action that can express force, reset, clean, branch deletion or
// file deletion (invariant 1). Subtasks stay in core.ProposalKind but
// ParseAction refuses them until their executor exists. The Phase 6 kinds
// (publish, ignore entries, review comment, init repo, pull) have frozen
// parsers, hashes and Describe text and are all Enabled. The new-repo kinds
// (init repo, publish) hand their work to a durable lifecycle: newrepo.go
// holds the pure plan and judgement of how such a proposal follows its
// operation.
package proposals

import "errors"

// ErrUnsupportedKind is returned for a kind that exists in core.ProposalKind
// but cannot run yet (publish, subtasks), and for unknown kinds.
var ErrUnsupportedKind = errors.New("that kind of proposal isn't available")
