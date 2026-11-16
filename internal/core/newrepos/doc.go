// Package newrepos is the functional core of creating a new repo under a
// tracked project and publishing it (NR-01 to NR-04,
// docs/design/phase-6-newrepos.md): a reserved repo row, a confirmed
// initialization (directory, git init, README, ignore file, first commit,
// tracking) and an optional confirmed publish (create remote, add origin, push),
// each stage durable and each unknown outcome reconciled read-only before any
// human retry.
//
// The package does no IO and generates no IDs. It imports only internal/core,
// so proposal hashes and repo status arrive as plain values. The shell in
// internal/newrepos interprets the commands through platform, git and host
// capabilities, and persists state, receipts and audit in one Store.Commit.
//
// Step is the full transition table of the design (step.go): it reserves, confirms,
// advances, reconciles and stops, and returns every unlisted pair unchanged.
package newrepos
