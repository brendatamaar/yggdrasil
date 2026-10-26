package safety

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// ResolveProgram resolves argv0 the way the platform will run it (PATH of env,
// PATHEXT on Windows, .cmd and .bat wrappers allowed) and returns the
// canonical long-form path for guard.Call.Program. The working directory is
// never searched: cwd is only used to refuse a relative PATH entry's reach and
// is otherwise ignored. A name with a path separator is refused (the allowlist
// matches bare names), and so is anything that is not installed. On failure
// the PathRef has OK=false and Raw set, so the guard answers PathUnresolved.
func ResolveProgram(argv0, cwd string, env []string) (guard.PathRef, error) {
	ref := guard.PathRef{Raw: argv0}
	if argv0 == "" || strings.ContainsAny(argv0, `/\`) || filepath.Base(argv0) != argv0 {
		return ref, errors.New("the command must be a plain program name without a folder")
	}
	// An empty dir keeps Resolve from preferring project wrappers or joining
	// the working directory; cwd is deliberately not passed.
	_ = cwd
	exe, err := platform.Resolve(argv0, "", env)
	if err != nil {
		return ref, fmt.Errorf("resolve program: %w", err)
	}
	canon, err := platform.ResolveRoot(exe)
	if err != nil {
		return ref, fmt.Errorf("resolve program: %w", err)
	}
	ref.Canon, ref.OK = canon, true
	return ref, nil
}
