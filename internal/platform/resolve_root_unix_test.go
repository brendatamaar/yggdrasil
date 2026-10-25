//go:build !windows

package platform_test

import (
	"os"
	"testing"
)

func shortName(testing.TB, string) string { return "" }

func makeLink(t *testing.T, link, target string) bool {
	if err := os.Symlink(target, link); err != nil {
		t.Logf("no symlink: %v", err)
		return false
	}
	return true
}

func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}
