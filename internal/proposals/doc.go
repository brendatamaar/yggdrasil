// Package proposals runs the proposal lifecycle. The lifecycle decisions
// (create, confirm, reject, execute) live in internal/core/proposals; this
// package parses requests, stores the new state and its audit entry in one
// transaction, and executes the resulting commands.
package proposals
