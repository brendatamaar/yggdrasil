package safety

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// baseEnv are the names copied from Yggdrasil's own environment. Names
// compare case-insensitively, as on Windows. Anything not listed is dropped,
// including VIRTUAL_ENV, CONDA_PREFIX and PYTHONPATH, which would point a
// child at the wrong interpreter.
var baseEnv = []string{
	"PATH", "PATHEXT", "SYSTEMROOT", "SYSTEMDRIVE", "COMSPEC", "WINDIR",
	"TEMP", "TMP", "TMPDIR",
	"USERPROFILE", "HOME", "HOMEDRIVE", "HOMEPATH", "USERNAME", "USER", "LOGNAME",
	"APPDATA", "LOCALAPPDATA", "PROGRAMDATA",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "OS",
	"SHELL", "LANG", "LC_ALL", "TERM", "TZ",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
}

// baseEnvPrefixes cover ProgramFiles, ProgramFiles(x86), ProgramW6432 and
// CommonProgramFiles*.
var baseEnvPrefixes = []string{"PROGRAMFILES", "COMMONPROGRAMFILES", "PROGRAMW6432", "COMMONPROGRAMW6432"}

func inBaseEnv(upper string) bool {
	if slices.Contains(baseEnv, upper) {
		return true
	}
	for _, p := range baseEnvPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// secretMarkers in a name mark it as a token, key or password.
var secretMarkers = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "CREDENTIAL", "PRIVATE_KEY"}

// looksSecret reports whether a variable or log attribute name looks like it
// holds a secret.
func looksSecret(name string) bool {
	up := strings.ReplaceAll(strings.ToUpper(name), "-", "_")
	if strings.HasSuffix(up, "_KEY") {
		return true
	}
	for _, m := range secretMarkers {
		if strings.Contains(up, m) {
			return true
		}
	}
	return false
}

type envBuilder struct {
	environ func() []string
}

// NewEnvBuilder returns the EnvBuilder that reads the process environment.
func NewEnvBuilder() EnvBuilder { return envBuilder{environ: os.Environ} }

// Build implements EnvBuilder. The result is sorted and never nil, so
// os/exec can not fall back to our own environment.
func (b envBuilder) Build(opts EnvOptions) ([]string, error) {
	allow := map[string]bool{}
	for _, n := range opts.Allow {
		allow[strings.ToUpper(n)] = true
	}
	vars := map[string]string{} // upper-case name -> NAME=value
	for _, kv := range b.environ() {
		name, _, ok := strings.Cut(kv, "=")
		// Windows keeps entries such as "=C:" for per-drive directories.
		if !ok || name == "" {
			continue
		}
		if up := strings.ToUpper(name); inBaseEnv(up) {
			vars[up] = kv
		}
	}
	for name, v := range opts.Extra {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.Contains(v, "\x00") {
			return nil, fmt.Errorf("environment variable %q is not valid", name)
		}
		up := strings.ToUpper(name)
		if looksSecret(name) && !allow[up] {
			return nil, fmt.Errorf("environment variable %s looks like a token or secret and is not passed to child processes", name)
		}
		vars[up] = name + "=" + v
	}
	env := make([]string, 0, len(vars))
	for _, kv := range vars {
		env = append(env, kv)
	}
	slices.Sort(env)
	return env, nil
}
