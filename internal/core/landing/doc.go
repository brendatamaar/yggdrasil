// Package landing is the functional core of the two proposals that land an
// agent's work (RV-05, RV-06, PL-03; docs/design/landing.md): the local
// --no-ff merge into the main checkout (merge_local) and the removal of a
// worktree (remove_worktree). Each run is a small pure state machine whose
// commands are a closed set: there is no push, reset, force, branch or
// delete-by-path command, so an executor cannot take a step the machine did
// not emit. Nothing here does IO, reads the clock or logs; the executors in
// internal/proposals interpret the commands. The types carry no JSON tags.
package landing
