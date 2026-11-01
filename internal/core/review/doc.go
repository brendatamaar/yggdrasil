// Package review is the functional core of review notes (RV-01,
// docs/design/changesets.md): a note anchored to a file and line of a task's
// diff, its sealed state (open, sending, sent, outdated), the inputs that
// move it, the commands it asks for, and the pure composer that turns notes
// into one message for the agent. Nothing here does IO, reads the clock or
// logs; the shell in internal/changesets interprets the commands. The types
// carry no JSON tags: wire DTOs live in internal/api.
package review
