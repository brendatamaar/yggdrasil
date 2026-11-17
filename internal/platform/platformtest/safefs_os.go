package platformtest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// CanonicalTempDir returns a temp folder whose path has no link and no 8.3
// short name on the way, as the safe filesystem capabilities require
// (t.TempDir() can sit behind /var -> /private/var or C:\Users\RUNNER~1).
func CanonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err = platform.CanonicalPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// OSFixture is the FSFixture of the real disk.
func OSFixture() FSFixture {
	return FSFixture{
		Mkdir: func(t *testing.T, dir string) {
			t.Helper()
			if err := os.MkdirAll(extended(dir), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		WriteFile: func(t *testing.T, path string, data []byte) {
			t.Helper()
			if err := os.WriteFile(extended(path), data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		ReadFile: func(t *testing.T, path string) ([]byte, bool) {
			t.Helper()
			data, err := os.ReadFile(extended(path))
			if errors.Is(err, os.ErrNotExist) {
				return nil, false
			}
			if err != nil {
				t.Fatal(err)
			}
			return data, true
		},
		Symlink: func(t *testing.T, link, target string) bool {
			t.Helper()
			if err := os.Symlink(target, extended(link)); err != nil {
				t.Logf("cannot create a symlink here, clause skipped: %v", err)
				return false
			}
			return true
		},
		Move: func(t *testing.T, from, to string) {
			t.Helper()
			if err := os.Rename(extended(from), extended(to)); err != nil {
				t.Fatal(err)
			}
		},
		Lock: osLock2,
	}
}

func osLock2(t *testing.T, path string) func() {
	t.Helper()
	return osLockFile(t, extended(path))
}

// osInitFS wraps the real SafeInitFS with the OS fixture.
type osInitFS struct{ platform.SafeInitFS }

func (osInitFS) Fixture() FSFixture { return OSFixture() }

// OSSafeInitFS is platform.NewSafeInitFS() with the real-disk fixture, the
// value to hand to RunSafeInitFSContract.
func OSSafeInitFS() platform.SafeInitFS { return osInitFS{platform.NewSafeInitFS()} }

type osIgnoreWriter struct{ platform.IgnoreWriter }

func (osIgnoreWriter) Fixture() FSFixture { return OSFixture() }

// OSIgnoreWriter is platform.NewIgnoreWriter() with the real-disk fixture.
func OSIgnoreWriter() platform.IgnoreWriter { return osIgnoreWriter{platform.NewIgnoreWriter()} }
