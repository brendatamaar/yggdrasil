//go:build !windows

package terminal_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	tt "github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
)

// TestRealUnixPTYContract runs the PTY contract against platform.StartPTY
// (creack/pty) with sh. The Windows twin (ConPTY, cmd.exe) is in
// contract_windows_test.go; both must satisfy the same suite as the fake.
func TestRealUnixPTYContract(t *testing.T) {
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sh, err := platform.Resolve("sh", dir, env) // the platform package finds programs; tests never look at PATH themselves
	if err != nil {
		t.Skip("sh is not installed")
	}
	tt.PTYContract(t, platform.StartPTY, tt.PTYProgram{
		Argv: []string{sh}, Dir: dir, Env: env,
		ExitInput: "exit 3\r", ExitCode: 3,
	})
}
