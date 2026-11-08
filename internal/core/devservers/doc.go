// Package devservers is the functional core of dev servers (DV-01 to DV-03,
// DV-05, docs/design/devservers.md): the state of one server, the inputs that
// move it, and the commands it asks for. Nothing here does IO, reads the
// clock or logs; internal/devservers interprets the commands. No JSON tags:
// wire DTOs live in internal/api.
package devservers
