// Package changesets is the functional core of change sets (RV-03, RV-04,
// docs/design/changesets.md): the state of one change set (open, confirming a
// run, shipped, closed), the inputs that move it, the commands it asks for,
// and the pure helpers for the confirm plan, merge order, dependency
// warnings and the linked-MR block. Nothing here does IO, reads the clock or
// logs; the shell in internal/changesets interprets the commands. The types
// carry no JSON tags: wire DTOs live in internal/api.
//
// Domain packages under internal/core do not import each other (archtest), so
// repo dependency edges, proposal statuses and hashes arrive as fields of the
// inputs. Review notes live in internal/core/review.
package changesets
