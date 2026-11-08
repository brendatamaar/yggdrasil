// Package worktreeenv is the functional core of the worktree environment
// (WE-01 to WE-03, docs/design/worktree-env.md): three independent pure
// pieces, Allocate (port blocks), Profile (bootstrap file copy) and Compose
// (docker compose lifecycle). Nothing here does IO, reads the clock or logs;
// internal/worktreeenv interprets the commands.
package worktreeenv
