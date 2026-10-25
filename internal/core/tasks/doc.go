// Package tasks is the functional core of a task's lifecycle
// (docs/design/tasks.md): turns, attempts, stop, finish, usage and sub-agent
// caps for one task (Step), and the admission of waiting tasks across the
// board (Schedule). Nothing here does IO, reads the clock or logs; the shell
// in internal/tasks interprets the commands. The types carry no JSON tags:
// wire DTOs live in internal/api.
//
// Domain packages under internal/core do not import each other (archtest), so
// the result of guard.Admit arrives in Message.Admission as data, and the
// shell converts between the two packages' TurnID types.
//
// Foundations only: Step and Schedule are stubs until task K1; they return
// their state unchanged and no commands.
package tasks

import "errors"

// ErrNotImplemented is returned by boundary parsers that are still stubs.
var ErrNotImplemented = errors.New("tasks: not implemented yet")
