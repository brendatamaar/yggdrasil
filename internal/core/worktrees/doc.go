// Package worktrees is the functional core of Ensure (docs/AGENTS.md 5.11,
// PRD WE-04): which repos of a task still need a worktree, which name is
// tried next, when a name collision is retried or given up on, and what is
// left when a repo fails half way. Nothing here does IO, reads the clock,
// draws random numbers or logs. The shell in internal/worktrees runs the
// commands (git, store, folder checks, randomness) and feeds the results back
// as inputs.
//
// Names, ids and times arrive inside inputs. The types carry no JSON tags.
// The core never removes anything (safety invariant 1): there is no command
// that deletes a worktree, a branch or a folder.
package worktrees
