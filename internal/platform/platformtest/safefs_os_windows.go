//go:build windows

package platformtest

import (
	"os"
	"testing"
)

// osLockFile holds the file open for writing, the way an editor does. Go's
// os.OpenFile shares read and write but a second handle that wants write
// access with read-only sharing then gets a sharing violation.
func osLockFile(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	return func() { _ = f.Close() }
}
