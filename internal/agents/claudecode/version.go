package claudecode

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// cliVersion is a Claude Code release number.
type cliVersion struct{ major, minor, patch int }

// minVersion is the oldest CLI the adapter runs: 2.1.295 added
// onFailure "block" for hooks, which the guard depends on (docs/spikes.md B5).
var minVersion = cliVersion{2, 1, 295}

func (v cliVersion) older(o cliVersion) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

func (v cliVersion) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

// parseVersion reads the output of `claude --version`, for example
// "2.1.299 (Claude Code)". Only the leading MAJOR.MINOR.PATCH counts.
func parseVersion(out string) (cliVersion, bool) {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return cliVersion{}, false
	}
	parts := strings.Split(fields[0], ".")
	if len(parts) != 3 {
		return cliVersion{}, false
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return cliVersion{}, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return cliVersion{}, false
			}
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return cliVersion{}, false
		}
		n[i] = v
	}
	return cliVersion{n[0], n[1], n[2]}, true
}

// checkCLI runs `claude --version` and refuses a CLI that does not start,
// prints an unreadable version, or is older than minVersion.
func checkCLI(ctx context.Context, bin string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out, errOut strings.Builder
	p, err := platform.Start(ctx, platform.Spec{Argv: []string{bin, "--version"}, Env: env, Stdout: &out, Stderr: &errOut})
	if err != nil {
		return &UserError{Message: "Claude Code couldn't be started. Run `claude --version` in a terminal to see why.", Err: err}
	}
	code, err := p.Wait()
	if err != nil || code != 0 {
		return &UserError{
			Message: fmt.Sprintf("Claude Code didn't start (exit code %d). Run `claude --version` in a terminal to see why, and sign in with `claude` if it asks.", code),
			Err:     err,
		}
	}
	v, ok := parseVersion(out.String())
	if !ok {
		return &UserError{Message: fmt.Sprintf("Yggdrasil couldn't read Claude Code's version. Update Claude Code to %s or newer, then try again.", minVersion)}
	}
	if v.older(minVersion) {
		return &UserError{Message: fmt.Sprintf("Claude Code %s is too old. Update it to %s or newer (run `claude update`), then try again.", v, minVersion)}
	}
	return nil
}
