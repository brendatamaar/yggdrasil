package claudecode

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/agents/agentstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// The shared adapter contract, run over the adapter with the test binary as
// the fake `claude` replaying recorded streams. No real CLI, no model API.
func TestAdapterContract(t *testing.T) {
	agentstest.AdapterContract(t, func(k agentstest.Kind) (agentstest.Subject, error) {
		stream := ""
		mode := "stream"
		switch k {
		case agentstest.KindPlain:
			stream = "live-subagent.jsonl"
		case agentstest.KindEdits:
			stream = "contract-edits.jsonl"
		case agentstest.KindNested:
			stream = "nested.jsonl"
		case agentstest.KindError:
			mode = "exit3"
		case agentstest.KindCancel:
			mode = "sleep"
		case agentstest.KindMissingPrereq:
			return agentstest.Subject{Adapter: New(Options{Binary: "no-such-claude-binary-xyz"})}, nil
		default:
			return agentstest.Subject{}, agentstest.ErrUnsupported
		}
		extra := map[string]string{fakeEnv: mode}
		if stream != "" {
			extra[fakeStream] = mustAbs(t, "testdata/"+stream)
		}
		env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: extra})
		if err != nil {
			return agentstest.Subject{}, err
		}
		dir := t.TempDir()
		exe := testExe(t)
		return agentstest.Subject{
			Adapter: New(Options{Binary: exe, HookExecutable: exe, TempDir: t.TempDir()}),
			Options: agents.StartOptions{
				Prompt: "Find the order routes.", Mode: core.ModeAsk, Dir: dir, Env: env,
				MaxSubagentDepth: 2, MaxConcurrentSubagents: 4,
				Policy: policyFor(t, core.ModeAsk, dir),
			},
		}, nil
	})
}
