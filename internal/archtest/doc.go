// Package archtest holds tests that read the module's own source and fail when
// an architecture rule of docs/AGENTS.md is broken: where processes may start,
// who may read the environment, which package may import which, and who may
// call the unredacted audit insert. It has no code of its own.
package archtest
