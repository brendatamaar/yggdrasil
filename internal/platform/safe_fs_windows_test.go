//go:build windows

package platform

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Windows-only clauses of the safe filesystem capabilities. The shared
// contract suites run on Windows from safe_fs_test.go; these cover what only
// Windows has: junctions, 8.3 aliases, \\wsl$ and device paths.

func mkJunction(t *testing.T, link, target string) {
	t.Helper()
	var out bytes.Buffer
	p, err := Start(context.Background(), Spec{
		Argv: []string{filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "mklink", "/J", link, target},
		Env:  helperEnv(), Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Skipf("can't create a junction here: exit %d, %v: %s", code, err, out.String())
	}
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	dir, err := CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSafeInitFSRefusesJunctionAncestor(t *testing.T) {
	base := canonicalTemp(t)
	real := filepath.Join(base, "real folder")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "junction")
	mkJunction(t, link, real)
	ctx := context.Background()
	s := NewSafeInitFS()
	if _, err := s.CreateChildDir(ctx, link, "shop-api"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("junction as parent: %v", err)
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Fatalf("something was created through the junction: %v", entries)
	}
	// A junction swapped in for the created folder is a change, not a success.
	id, err := s.CreateChildDir(ctx, real, "shop-api")
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(real, "shop-api.old")
	if err := os.Rename(filepath.Join(real, "shop-api"), moved); err != nil {
		t.Fatal(err)
	}
	mkJunction(t, filepath.Join(real, "shop-api"), moved)
	if err := s.VerifyChildDir(ctx, real, "shop-api", id); !errors.Is(err, ErrChanged) {
		t.Fatalf("junction in place of the folder: %v", err)
	}
}

func TestSafeInitFSRefusesShortNameAndWSLAndDevicePaths(t *testing.T) {
	base := canonicalTemp(t)
	long := filepath.Join(base, "a long folder name for short")
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	src, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(src, &buf[0], uint32(len(buf))) //nolint:gosec // small buffer
	ctx := context.Background()
	s := NewSafeInitFS()
	if err == nil && n > 0 {
		if short := windows.UTF16ToString(buf[:n]); short != long {
			if _, err := s.CreateChildDir(ctx, short, "x"); !errors.Is(err, ErrUnsafePath) {
				t.Errorf("8.3 alias %q: %v", short, err)
			}
		}
	} else {
		t.Log("8.3 names are disabled on this volume; alias clause skipped")
	}
	for _, p := range []string{`\\wsl$\Ubuntu\home\me`, `\\wsl.localhost\Ubuntu\home`, `\\.\C:\x`, `\\?\GLOBALROOT\Device\x`} {
		if _, err := s.CreateChildDir(ctx, p, "x"); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := s.CreateChildDir(ctx, `\\server\share\x`, "x"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("UNC share: %v", err)
	}
	// The extended-length spelling of a normal folder works.
	if _, err := s.CreateChildDir(ctx, `\\?\`+long, "ext"); err != nil {
		t.Errorf(`\\?\ spelling: %v`, err)
	}
}

func TestIgnoreWriterRefusesJunctionNamedGitignore(t *testing.T) {
	root := canonicalTemp(t)
	other := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	mkJunction(t, filepath.Join(root, ".gitignore"), other)
	w := NewIgnoreWriter()
	if _, err := w.ReadIgnore(context.Background(), root); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("read: %v", err)
	}
	if _, _, err := w.AppendIgnore(context.Background(), root, []string{"x"}, IgnoreExpectation{Absent: true}); err == nil {
		t.Fatal("append through a junction succeeded")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("something was written through the junction: %v", entries)
	}
}

func TestIgnoreWriterRefusesWSLRoot(t *testing.T) {
	_, err := NewIgnoreWriter().ReadIgnore(context.Background(), `\\wsl$\Ubuntu\home\me\repo`)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("got %v", err)
	}
}
