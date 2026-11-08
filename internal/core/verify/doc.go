// Package verify is the functional core of automatic verification (VF-01 to
// VF-03, WE-05, BL-08, BL-09; docs/design/verify.md): one Verification per
// (task, repo), its sealed State, the inputs that move it and the commands it
// asks for. Nothing here does IO, reads the clock or logs; the shell in
// internal/verify interprets the commands. The types carry no JSON tags: wire
// DTOs live in internal/api.
//
// Domain packages under internal/core do not import each other (archtest), so
// the tasks' turn IDs, repo settings and git facts arrive as fields of the
// inputs (Facts, Plan) and the shell converts between packages.
package verify
