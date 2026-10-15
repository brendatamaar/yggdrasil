package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// ErrNotInstalled is returned by New when git is not on the PATH of env.
var ErrNotInstalled = errors.New("git is not installed")

// baseConfig is passed to every call (WE-04). It sets no autocrlf: the user's
// own setting stays. An empty credential.helper disables every helper, so git
// never asks for or stores a secret (invariant 3, 4).
var baseConfig = []string{
	"-c", "core.longpaths=true",
	"-c", "core.quotepath=false",
	"-c", "credential.helper=",
}

// Git runs the safe subset of the git CLI. It is safe for concurrent use.
type Git struct {
	exe    string
	env    []string
	taskID string
	redact safety.Redactor
}

// Option configures New.
type Option func(*Git)

// WithTaskID starts every git process in that task's job, so cancelling the
// task kills them (docs/AGENTS.md 5.3).
func WithTaskID(id string) Option { return func(g *Git) { g.taskID = id } }

// New returns a Git that starts git with env, the clean environment from
// internal/safety. A nil env is refused (platform.ErrNoEnv). Terminal prompts
// are disabled on top of env.
func New(env []string, opts ...Option) (*Git, error) {
	if env == nil {
		return nil, fmt.Errorf("git: %w", platform.ErrNoEnv)
	}
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			return nil, fmt.Errorf("%w: %w", ErrNotInstalled, err)
		}
		return nil, fmt.Errorf("find git: %w", err)
	}
	g := &Git{
		exe: exe,
		// Fresh slice: never append into the caller's backing array.
		env:    append(append(make([]string, 0, len(env)+1), env...), "GIT_TERMINAL_PROMPT=0"),
		redact: safety.NewRedactor(),
	}
	for _, o := range opts {
		o(g)
	}
	return g, nil
}

// run starts git in dir and returns stdout. readOnly adds --no-optional-locks
// so a status poll never blocks a user's own git. Arguments never hold a token.
func (g *Git) run(ctx context.Context, dir, op string, readOnly bool, args ...string) (string, error) {
	argv := append([]string{g.exe}, baseConfig...)
	if readOnly {
		argv = append(argv, "--no-optional-locks")
	}
	argv = append(argv, args...)

	var stdout, stderr bytes.Buffer
	p, err := platform.Start(ctx, platform.Spec{
		Argv: argv, Dir: dir, Env: g.env, TaskID: g.taskID,
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		return "", &Error{Op: op, Err: err, UserMsg: startMessage(err)}
	}
	code, err := p.Wait()
	if err != nil {
		return "", &Error{Op: op, Err: err, UserMsg: fmt.Sprintf("git %s did not finish. Try again.", op)}
	}
	if ctx.Err() != nil {
		return "", &Error{Op: op, Err: ctx.Err(), UserMsg: fmt.Sprintf("git %s was cancelled.", op)}
	}
	if code != 0 {
		se := strings.TrimSpace(g.redact.Redact(stderr.String()))
		return "", &Error{Op: op, Code: code, Stderr: se, Err: errors.New("exit status " + fmt.Sprint(code)), UserMsg: classify(op, se)}
	}
	return stdout.String(), nil
}

func startMessage(err error) string {
	if errors.Is(err, platform.ErrWSLPath) {
		return err.Error()
	}
	return "Yggdrasil could not start git. Check that this folder exists and git is installed."
}

// classify turns git's stderr into advice. Text may vary by git version and
// language, so the generic fallback matters most.
func classify(op, stderr string) string {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "not a git repository"):
		return "This folder is not a git repository. Check the repo path."
	case strings.Contains(low, "dubious ownership"):
		return "Git does not trust this folder because another user owns it. Mark it safe in git (safe.directory) and try again."
	case strings.Contains(low, "could not read from remote") || strings.Contains(low, "could not resolve host") ||
		strings.Contains(low, "authentication failed") || strings.Contains(low, "terminal prompts disabled"):
		return "Git could not reach the remote or needs credentials. Check your network and sign-in (SSH key or credential setup), then try again."
	case strings.Contains(low, "index.lock"):
		return "Another git process is using this repo. Wait for it to finish, then try again."
	}
	return fmt.Sprintf("git %s failed. See the details, fix the problem and try again.", op)
}

// Error is a failed git call. Error() is for logs; UserMessage() is for the
// user and says what to do next. Stderr is already redacted.
type Error struct {
	Op      string
	Code    int
	Stderr  string
	UserMsg string
	Err     error
}

func (e *Error) Error() string {
	s := "git " + e.Op + ": " + e.Err.Error()
	if e.Stderr != "" {
		s += ": " + e.Stderr
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// UserMessage returns the plain-language message.
func (e *Error) UserMessage() string { return e.UserMsg }

// UserMessage returns the plain-language message of an error from this
// package, or err's text for any other error.
func UserMessage(err error) string {
	if u, ok := errors.AsType[interface {
		error
		UserMessage() string
	}](err); ok {
		return u.UserMessage()
	}
	return err.Error()
}

// ErrInvalidArg is returned for a ref, branch or remote name that looks like a
// git option or is empty.
var ErrInvalidArg = errors.New("invalid git argument")

// checkName refuses empty names and names starting with "-", which git would
// read as an option.
func checkName(kind, v string) error {
	if v == "" || strings.HasPrefix(v, "-") || strings.ContainsRune(v, 0) {
		return &Error{Op: "check " + kind, Err: fmt.Errorf("%w: %q", ErrInvalidArg, v),
			UserMsg: fmt.Sprintf("The %s name %q is not valid. Choose another name.", kind, v)}
	}
	return nil
}
