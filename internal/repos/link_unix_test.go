//go:build !windows

package repos_test

import (
	"os"
	"testing"
)

// makeLink makes a symlink.
func makeLink(t *testing.T, link, target string) bool {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Logf("symlink: %v", err)
		return false
	}
	return true
}
