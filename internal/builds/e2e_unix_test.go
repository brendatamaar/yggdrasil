//go:build !windows

package builds_test

import (
	"os"
	"path/filepath"
	"testing"
)

// writeHelper writes a tiny shell program in dir/tools and returns its argv[0],
// relative to dir.
func writeHelper(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "tools", "hello.sh")
	if err := mkfile(p, "#!/bin/sh\necho built in $PWD\necho token "+sentinel+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return "tools/hello.sh"
}
