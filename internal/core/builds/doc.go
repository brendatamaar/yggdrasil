// Package builds is the functional core of builds (BL-03 to BL-09,
// docs/design/builds.md): the state of one build, the inputs that move it, the
// commands it asks for, and the pure queue-admission and module-ordering
// decisions. Nothing here does IO, reads the clock or logs; the shell in
// internal/builds interprets the commands. The types carry no JSON tags:
// wire DTOs live in internal/api.
package builds
