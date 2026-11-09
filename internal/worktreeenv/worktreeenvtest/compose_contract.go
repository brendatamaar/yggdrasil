package worktreeenvtest

import (
	"context"
	"testing"

	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// ComposeRunnerEnv is one ComposeRunner under test.
type ComposeRunnerEnv struct {
	// Runner is a working runner, or nil when this implementation cannot run
	// here (the real runner without Docker). Cases that need it are skipped.
	Runner worktreeenv.ComposeRunner
	// NoDocker is a runner on a machine where Docker is not installed.
	NoDocker worktreeenv.ComposeRunner
	// Dir is an existing folder to run in.
	Dir string
}

// ComposeRunnerContract runs the ComposeRunner contract against a fake and the
// real runner.
func ComposeRunnerContract(t *testing.T, mk func(t *testing.T) ComposeRunnerEnv) {
	t.Helper()
	ctx := context.Background()
	call := func(e ComposeRunnerEnv, op env.ComposeOp) worktreeenv.ComposeCall {
		return worktreeenv.ComposeCall{Op: op, Project: "ygg-contract", File: "compose.yml", Dir: e.Dir,
			Env: map[string]string{"YGG_PORT_BASE": "20000", "YGG_PORT_0": "20000"}}
	}

	t.Run("an operation outside the closed set is refused", func(t *testing.T) {
		e := mk(t)
		for _, r := range []worktreeenv.ComposeRunner{e.NoDocker, e.Runner} {
			if r == nil {
				continue
			}
			c := call(e, "down -v")
			if res := r.Run(ctx, c); res.Failure != env.FailComposeError {
				t.Fatalf("got %+v", res)
			}
		}
	})

	t.Run("missing docker is DockerMissing with a plain message", func(t *testing.T) {
		e := mk(t)
		for _, op := range []env.ComposeOp{env.OpUp, env.OpStop, env.OpDown, env.OpPs} {
			res := e.NoDocker.Run(ctx, call(e, op))
			if res.Failure != env.FailDockerMissing || res.Summary != worktreeenv.MsgDockerMissing {
				t.Fatalf("%s: got %+v", op, res)
			}
		}
	})

	t.Run("a failed run reports a failure and a short summary", func(t *testing.T) {
		e := mk(t)
		if e.Runner == nil {
			t.Skip("Docker is not available here")
		}
		// The compose file does not exist, so every operation fails (or the
		// daemon is down); either way it is a failure, never a success.
		res := e.Runner.Run(ctx, call(e, env.OpUp))
		if res.Failure == "" || res.Summary == "" || len(res.Summary) > 600 {
			t.Fatalf("got %+v", res)
		}
	})
}
