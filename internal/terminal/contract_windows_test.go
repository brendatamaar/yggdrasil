//go:build windows

package terminal_test

import (
	"os"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	tt "github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
)

// TestRealPTYContract runs the PTY contract against platform.StartPTY (ConPTY)
// with cmd.exe. It only builds on Windows and has not been run by the author
// (written on Linux); the Windows CI runner and the manual checklist cover it.
func TestRealPTYContract(t *testing.T) {
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = `C:\Windows\System32\cmd.exe`
	}
	tt.PTYContract(t, platform.StartPTY, tt.PTYProgram{
		Argv: []string{shell}, Dir: t.TempDir(), Env: env,
		ExitInput: "exit 3\r\n", ExitCode: 3,
	})
}
