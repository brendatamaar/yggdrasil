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
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o600) })
}

func mkLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks not available: " + err.Error())
	}
}
