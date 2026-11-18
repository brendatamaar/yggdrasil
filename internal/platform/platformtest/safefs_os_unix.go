//go:build !windows

package platformtest

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// osLockFile holds the advisory lock IgnoreWriter takes, the way a
// cooperating program would, and returns the release func.
func osLockFile(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test helper on a temp path
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	return func() { _ = f.Close() }
}
