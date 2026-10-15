package safety

// This file is the contract of the safety package. Implementations arrive in
// Phase 1 (clean environment, redaction) and Phase 3 (path guard, command
// allowlist, Ask-mode integrity check).

// EnvOptions configure the clean environment (SF-04, docs/AGENTS.md 5.3).
type EnvOptions struct {
	// Extra variables the caller wants set. A name that looks like a token or
	// secret is refused unless Allow lists it.
	Extra map[string]string
	// Allow names Extra variables that may match the secret patterns, for
	// example a proxy setting.
	Allow []string
}

// EnvBuilder builds the environment of every child process. It copies a
// small allowlist from Yggdrasil's own environment (PATH, system variables,
// temp dirs), drops VIRTUAL_ENV, CONDA_PREFIX and PYTHONPATH, and never
// passes a token. The result is never nil. Only this package may read
// os.Environ.
type EnvBuilder interface {
	Build(opts EnvOptions) ([]string, error)
}

// Redactor removes tokens from text before it reaches a log or the audit
// log (invariant 4): bearer and basic credentials, URLs with credentials,
// well-known token formats and the values of configured tokens.
type Redactor interface {
	Redact(s string) string
}
