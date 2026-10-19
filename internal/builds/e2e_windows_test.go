//go:build windows

package builds_test

import (
	"path/filepath"
	"testing"
)

// writeHelper writes a tiny batch program in dir/tools and returns its argv[0],
// relative to dir.
func writeHelper(t *testing.T, dir string) string {
	t.Helper()
	if err := mkfile(filepath.Join(dir, "tools", "hello.cmd"), "@echo off\r\necho built in %CD%\r\necho token "+sentinel+"\r\nexit /b 0\r\n"); err != nil {
		t.Fatal(err)
	}
	return "tools/hello.cmd"
}
