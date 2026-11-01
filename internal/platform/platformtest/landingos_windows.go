//go:build windows

package platformtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// osLock opens a file inside dir the way an editor does: Go opens files with
// sharing for read and write but not for delete, so renaming or deleting
// the file or any folder above it fails.
func osLock(t *testing.T, dir string) func() {
	t.Helper()
	f, err := os.Open(extended(filepath.Join(dir, "a.txt")))
	if err != nil {
		t.Fatal(err)
	}
	return func() { _ = f.Close() }
}

func osShort(t *testing.T, p string) string {
	t.Helper()
	src, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(src, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return ""
	}
	s := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(s, p) {
		return ""
	}
	return s
}
