// Package repos is the functional core of repo tracking and repo status
// (WS-01 to WS-04, docs/design/repo-tracking.md and docs/design/repo-status.md).
//
// Tracking decides on a Workspace (every project and repo row, tracked or
// not): Decide returns the new workspace, commands as data and the outcome for
// the caller. Status keeps a Refresher (what to read next, with bounded
// concurrency) and derives flags and counts. Nothing here touches the disk,
// reads the clock or logs; the shell in internal/repos does. The types carry
// no JSON tags: wire DTOs live in internal/api.
package repos
