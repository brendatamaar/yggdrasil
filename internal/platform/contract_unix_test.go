//go:build !windows

package platform_test

import (
	"strconv"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// The real Start must pass the same contract as the fake; on Unix the
// commands are /bin/sh one-liners.
func TestRealStartMeetsProcessContract(t *testing.T) {
	sh := func(script string, args ...string) []string {
		return append([]string{"/bin/sh", "-c", script, "sh"}, args...)
	}
	platformtest.ProcessContract(t, platform.Start, platformtest.Commands{
		Env:     []string{"PATH=/usr/bin:/bin"},
		Exit:    func(code int) []string { return sh("exit " + strconv.Itoa(code)) },
		Print:   func(text string) []string { return sh(`echo "$1"`, text) },
		Block:   func() []string { return sh("exec sleep 120") },
		Missing: []string{"ygg-no-such-program"},
	})
}
