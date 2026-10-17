// Package core is the functional core: domain types (projects, repos,
// worktrees, tasks, change sets, proposals) and pure decisions on them.
// Subpackages per domain hold step functions (state + input → new state +
// commands as data). Nothing here does IO, reads the clock, draws random
// values, starts goroutines or logs (docs/AGENTS.md 5.11). It must not import
// api, web or any agent adapter package.
package core
