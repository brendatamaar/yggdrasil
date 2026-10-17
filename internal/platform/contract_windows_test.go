//go:build windows

package platform_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// The real Start must pass the same contract as the fake. The commands are the
// helper modes of the test binary (TestMain in platform_windows_test.go, which
// is shared by this external test package).
func TestRealStartMeetsProcessContract(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := func(args ...string) []string { return append([]string{exe}, args...) }
	platformtest.ProcessContract(t, platform.Start, platformtest.Commands{
		Env: []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"TEMP=" + os.TempDir(),
			"TMP=" + os.TempDir(),
			"YGG_PLATFORM_HELPER=1", // helperEnvVar
		},
		Exit:    func(code int) []string { return helper("exit", strconv.Itoa(code)) },
		Print:   func(text string) []string { return helper("echo", text) },
		Block:   func() []string { return helper("sleep", "2m") },
		Missing: []string{"ygg-no-such-program"},
	})
}
