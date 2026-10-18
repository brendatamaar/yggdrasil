package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNoRemote is returned by RemoteURL when the repo has no remote of that
// name. The repo is fine: it simply isn't linked to a host.
var ErrNoRemote = errors.New("no such remote")

// RemoteURL returns the fetch URL of a remote, for linking a repo to a git
// host (GH-01). It is read-only. The URL can hold credentials a user typed
// into the remote: the caller must parse it and never log or show the raw
// value (invariant 4).
func (g *Git) RemoteURL(ctx context.Context, dir, name string) (string, error) {
	if err := checkName("remote", name); err != nil {
		return "", err
	}
	out, err := g.run(ctx, dir, "remote url", true, "remote", "get-url", "--end-of-options", name)
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && ge.Code != 0 && strings.Contains(strings.ToLower(ge.Stderr), "no such remote") {
			return "", &Error{
				Op: ge.Op, Code: ge.Code, Err: fmt.Errorf("%w: %q", ErrNoRemote, name),
				UserMsg: fmt.Sprintf("This repo has no remote called %q, so it can't be linked to a git host.", name),
			}
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}
