// Package newrepos is the imperative shell of creating and publishing a new
// repo (NR-01 to NR-04, docs/design/phase-6-newrepos.md). It will parse specs
// into core/newrepos types, persist the reservation, setup draft and stage
// receipts through db.Store.Commit, and run the stage commands of
// core/newrepos.Step through the safe filesystem, git and host capabilities,
// with a durable worker, shared main lock and read-only reconciliation of every
// unknown outcome. This foundation declares only the ports (ports.go); the
// new-repo task (N) implements the service.
package newrepos
