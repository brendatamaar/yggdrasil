//go:build !windows

package safety_test

import (
	"os"
	"testing"
)

func denyRead(t *testing.T, file string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	// Restore the exact original mode: a directory is 0o755, not 0o600.
	mode := info.Mode().Perm()
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, mode) })
}

func mkLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not available: " + err.Error())
	}
}
