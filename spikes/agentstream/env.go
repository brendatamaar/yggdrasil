package agentstream

import (
	"fmt"
	"slices"
	"strings"
)

// BaseEnvAllowlist is what the child inherits from our environment. Nothing
// else does: the env is built name by name, never from os.Environ().
var BaseEnvAllowlist = []string{
	"SystemRoot", "PATH", "PATHEXT", "ComSpec", "TEMP", "TMP",
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH",
}

// deniedEnv must never reach a child, not even as an explicit extra.
var deniedEnv = []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "GITEA_TOKEN"}

// secretMarkers reject extras that look like credentials. This also blocks
// ANTHROPIC_API_KEY: whether the CLI gets one is an open question for the
// lead, not decided here.
var secretMarkers = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "CREDENTIAL"}

// CleanEnv builds a child environment: the allowlisted names found through
// lookup (os.LookupEnv in production) plus extras. An extra that is a known
// token or looks like a secret is an error. Names compare case-insensitively,
// as on Windows; an extra replaces an allowlisted value. The result is never
// nil, so os/exec can't fall back to our environment.
func CleanEnv(lookup func(string) (string, bool), extras map[string]string) ([]string, error) {
	env := []string{}
	seen := map[string]bool{}
	for name, v := range extras {
		if err := checkExtra(name); err != nil {
			return nil, err
		}
		env = append(env, name+"="+v)
		seen[strings.ToUpper(name)] = true
	}
	for _, name := range BaseEnvAllowlist {
		if seen[strings.ToUpper(name)] {
			continue
		}
		if v, ok := lookup(name); ok {
			env = append(env, name+"="+v)
		}
	}
	slices.Sort(env)
	return env, nil
}

func checkExtra(name string) error {
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return fmt.Errorf("env name %q is not valid", name)
	}
	up := strings.ToUpper(name)
	if slices.Contains(deniedEnv, up) {
		return fmt.Errorf("env %s is a git-host token and never reaches a child process", name)
	}
	for _, m := range secretMarkers {
		if strings.Contains(up, m) {
			return fmt.Errorf("env %s looks like a secret; it isn't passed to the child", name)
		}
	}
	return nil
}
