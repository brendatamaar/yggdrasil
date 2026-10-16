package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned by Resolve when the program is not installed.
var ErrNotFound = errors.New("program not found")

// Resolve finds the executable for name (docs/AGENTS.md 9). In dir it prefers
// the project's own wrapper: mvn gives mvnw.cmd (./mvnw), gradle gives
// gradlew.bat (./gradlew), python gives .venv\Scripts\python.exe
// (.venv/bin/python). Otherwise it searches the PATH of env, with PATHEXT on
// Windows, so npm gives npm.cmd. The current directory is never searched.
// env must not be nil (ErrNoEnv).
func Resolve(name, dir string, env []string) (string, error) {
	if env == nil {
		return "", ErrNoEnv
	}
	if dir != "" && filepath.Base(name) == name {
		if w := wrapperFile(name); w != "" {
			if p := filepath.Join(dir, w); isRegular(p) {
				return p, nil
			}
		}
	}
	return lookPath(name, dir, env)
}

// wrapperFile maps a tool name to its project-local wrapper, relative to the
// project directory. "" means the tool has none.
func wrapperFile(name string) string {
	switch strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))) {
	case "mvn", "mvnw":
		return mvnwFile
	case "gradle", "gradlew":
		return gradlewFile
	case "python", "python3":
		return venvPython
	}
	return ""
}

// lookPath resolves name like a shell would, but with the PATH of env instead
// of ours. A name with a separator is taken relative to dir.
func lookPath(name, dir string, env []string) (string, error) {
	if name == "" {
		return "", errors.New("platform: empty program name")
	}
	if filepath.Base(name) != name {
		if !filepath.IsAbs(name) && dir != "" {
			name = filepath.Join(dir, name)
		}
		return firstExecutable([]string{name}, env, name)
	}
	var dirs []string
	for _, d := range filepath.SplitList(envGet(env, "PATH")) {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, name))
		}
	}
	return firstExecutable(dirs, env, name)
}

func firstExecutable(bases []string, env []string, name string) (string, error) {
	for _, b := range bases {
		for _, c := range candidates(b, env) {
			if isExecutable(c) {
				return c, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %q is not installed or not on the PATH. Install it, or add its folder to the PATH", ErrNotFound, name)
}

// envGet returns the last value of key in env (names compare case-insensitively).
func envGet(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, key) {
			val = v
		}
	}
	return val
}

func isRegular(path string) bool {
	fi, err := os.Stat(path) //nolint:gosec // only stats; the path is a PATH or SystemRoot candidate
	return err == nil && fi.Mode().IsRegular()
}
