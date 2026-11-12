// Package actions is the functional core of workspace actions (AC-01 to AC-06,
// docs/design/phase-6-actions.md): the typed catalogue of what an agent can do
// about a "Needs attention" reason, and the pure dispatch machine that turns
// one explicit user submission into a draft task, a task or a set of proposals
// exactly once. It does no IO, reads no clock and generates no IDs: facts,
// time and IDs arrive as inputs; effects leave as commands that internal/actions
// interprets.
//
// This package imports only internal/core. It therefore carries its own small
// fact and context types instead of importing the host, repo or task domains;
// the shell maps those onto Facts and PreparedContext.
package actions
