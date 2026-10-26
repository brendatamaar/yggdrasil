//go:build !windows

package claudecode

import (
	"os"
	"testing"
)

// makeDirLink creates a symlink at link pointing to target.
func makeDirLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("can't create a link here: %v", err)
	}
}

// shortName: only Windows has 8.3 names.
func shortName(*testing.T, string) string { return "" }
