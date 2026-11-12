// Package actions is the imperative shell of workspace actions (AC-01 to AC-06,
// docs/design/phase-6-actions.md). It will parse requests into core/actions
// types, compute the catalogue from scoped facts, load bounded redacted context
// and, after an explicit submission, dispatch one draft task, task or set of
// proposals exactly once, with the request's durable intent persisted before
// any effect. This foundation declares only the capabilities it will depend on
// (ports.go); the orchestration task (A) implements the service.
package actions
