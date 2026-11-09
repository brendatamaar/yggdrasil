//go:build !windows

package terminal_test

import (
	"os/exec"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	tt "github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
)

// TestRealUnixPTYContract runs the PTY contract against platform.StartPTY
// (creack/pty) with sh. The Windows twin (ConPTY, cmd.exe) is in
// contract_windows_test.go; both must satisfy the same suite as the fake.
func TestRealUnixPTYContract(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not installed")
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tt.PTYContract(t, platform.StartPTY, tt.PTYProgram{
		Argv: []string{sh}, Dir: t.TempDir(), Env: env,
		ExitInput: "exit 3\r", ExitCode: 3,
	})
}
