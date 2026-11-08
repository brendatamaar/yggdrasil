// Package terminal is the functional core of the terminal dock (DV-04,
// DV-05, docs/design/terminal.md): the registry of single-use tokens and
// sessions, the inputs that move it, and the commands it asks for. Nothing
// here does IO, reads the clock, draws random numbers or logs. The raw token
// never enters the core; only its SHA-256 (TokenHash) does.
package terminal
