package platform

import (
	"errors"
	"fmt"
)

// ErrNoPython is returned by PythonBase when no base interpreter is installed.
var ErrNoPython = errors.New("python is not installed")

// PythonBase finds the interpreter used to create a new .venv. On Windows it
// asks the py launcher (py -3) which interpreter it would run, and falls back
// to python on the PATH; elsewhere it uses python3, then python, on the PATH.
// It reads only the PATH (and PATHEXT) of env, which the caller took from the
// clean environment: VIRTUAL_ENV is never consulted, so a venv that Yggdrasil
// itself was started from cannot become the base of a project's venv.
func PythonBase(env []string) (string, error) {
	if env == nil {
		return "", ErrNoEnv
	}
	if p := launcherPython(env); p != "" {
		return p, nil
	}
	for _, name := range []string{"python3", "python"} {
		p, err := lookPath(name, "", env)
		if err == nil && !isStoreStub(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w. Install Python 3 (from python.org, or your package manager) and make sure it is on the PATH, then try again", ErrNoPython)
}
