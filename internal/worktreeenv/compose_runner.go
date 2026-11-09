package worktreeenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// ComposeCall is one compose operation. The command line is fixed by Op
// (env.Argv): nothing in the call can add an argument (invariant C2).
type ComposeCall struct {
	Op      env.ComposeOp
	Project env.ProjectName
	File    env.RelPath
	// Dir is the worktree folder; File is relative to it.
	Dir string
	// Env holds the extra variables (YGG_PORT_BASE, YGG_PORT_0..n). The runner
	// adds them to the clean environment; it never passes anything else.
	Env map[string]string
}

// ComposeResult is the outcome of one operation. Failure is empty on success.
type ComposeResult struct {
	Failure env.FailKind
	// Summary is a plain-language, redacted, path-free note for a failure.
	Summary string
	// Output is stdout, kept only for OpPs.
	Output []byte
}

// ComposeRunner runs one compose operation and waits for it. It never returns
// an error: a failure is a domain outcome in the result.
type ComposeRunner interface {
	Run(ctx context.Context, c ComposeCall) ComposeResult
}

// Plain-language messages of the failure kinds.
const (
	MsgDockerMissing = "Docker is not installed or not found. Install Docker Desktop, start it and try again."
	MsgDaemonDown    = "Docker is installed but not running. Start Docker Desktop and try again."
)

// PlatformComposeRunner runs `docker compose` through platform.Start with the
// clean environment from internal/safety.
type PlatformComposeRunner struct {
	Env     safety.EnvBuilder
	Redact  safety.Redactor
	Start   platform.StartFunc                                   // default platform.Start
	Resolve func(name, dir string, env []string) (string, error) // default platform.Resolve
	// Timeout bounds one operation; default 10 minutes (images may be pulled).
	Timeout time.Duration
}

// maxCompose caps the output kept from a compose run.
const maxCompose = 64 << 10

type capped struct {
	buf bytes.Buffer
}

func (c *capped) Write(p []byte) (int, error) {
	if room := maxCompose - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// Run implements ComposeRunner.
func (r PlatformComposeRunner) Run(ctx context.Context, c ComposeCall) ComposeResult {
	fail := func(k env.FailKind, msg string) ComposeResult { return ComposeResult{Failure: k, Summary: msg} }
	argv, err := env.Argv(c.Op, c.Project, c.File)
	if err != nil {
		return fail(env.FailComposeError, "That compose operation is not allowed.")
	}
	if r.Env == nil {
		return fail(env.FailComposeError, "Docker compose could not be started: no clean environment.")
	}
	clean, err := r.Env.Build(safety.EnvOptions{Extra: c.Env})
	if err != nil {
		return fail(env.FailComposeError, "Docker compose could not be started: the environment is not valid.")
	}
	resolve := r.Resolve
	if resolve == nil {
		resolve = platform.Resolve
	}
	exe, err := resolve(argv[0], c.Dir, clean)
	if errors.Is(err, platform.ErrNotFound) {
		return fail(env.FailDockerMissing, MsgDockerMissing)
	}
	if err != nil {
		return fail(env.FailComposeError, "Docker could not be found: "+r.redact(err.Error()))
	}
	argv[0] = exe
	start := r.Start
	if start == nil {
		start = platform.Start
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out, errOut capped
	p, err := start(ctx, platform.Spec{Argv: argv, Dir: c.Dir, Env: clean, Stdout: &out, Stderr: &errOut})
	if errors.Is(err, platform.ErrNotFound) {
		return fail(env.FailDockerMissing, MsgDockerMissing)
	}
	if err != nil {
		return fail(env.FailComposeError, "Docker compose could not be started: "+r.redact(err.Error()))
	}
	code, err := p.Wait()
	if err != nil || ctx.Err() != nil {
		_ = p.Kill()
		if ctx.Err() != nil {
			return fail(env.FailComposeError, "Docker compose took too long or was cancelled.")
		}
		return fail(env.FailComposeError, "Docker compose stopped unexpectedly.")
	}
	if code == 0 {
		res := ComposeResult{}
		if c.Op == env.OpPs {
			res.Output = out.buf.Bytes()
		}
		return res
	}
	text := errOut.buf.String()
	if text == "" {
		text = out.buf.String()
	}
	if daemonDown(text) {
		return fail(env.FailDaemonDown, MsgDaemonDown)
	}
	return fail(env.FailComposeError, fmt.Sprintf("Docker compose failed (exit %d). %s", code, r.redact(lastLines(text, 4, 400))))
}

func (r PlatformComposeRunner) redact(s string) string {
	if r.Redact == nil {
		return safety.NewRedactor().Redact(s)
	}
	return r.Redact.Redact(s)
}

func daemonDown(text string) bool {
	l := strings.ToLower(text)
	for _, m := range []string{
		"cannot connect to the docker daemon", "is the docker daemon running", "error during connect",
		"docker daemon is not running", "failed to connect to the docker api", "the docker daemon is not running",
	} {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// lastLines keeps the last n non-empty lines, at most max bytes, with the
// worktree-specific noise left to the redactor. Compose messages rarely hold
// paths, and the text is shown only to the user who started the operation.
func lastLines(s string, n, max int) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, " | ")
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}
