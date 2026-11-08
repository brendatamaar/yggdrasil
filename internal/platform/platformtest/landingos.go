package platformtest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// extended gives deep Windows paths the \\?\ form so a test can build them.
func extended(p string) string {
	if runtime.GOOS == "windows" && len(p) >= 240 && !strings.HasPrefix(p, `\\?\`) {
		return `\\?\` + p
	}
	return p
}

// OSLandingFixture is the fixture of platform.RealLanding: real folders below
// root, which must be an existing canonical folder (for example t.TempDir()
// after EvalSymlinks). Lock, Link and Short are set where the OS supports them.
func OSLandingFixture(root string) LandingFixture {
	fx := LandingFixture{Root: root}
	fx.Mkdir = func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(extended(dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(t *testing.T, p string, mode os.FileMode) {
		t.Helper()
		fx.Mkdir(t, filepath.Dir(p))
		if err := os.WriteFile(extended(p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if mode != 0o600 {
			if err := os.Chmod(extended(p), mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	fx.MakeTree = func(t *testing.T, dir string) {
		t.Helper()
		write(t, filepath.Join(dir, "a.txt"), 0o600)
		write(t, filepath.Join(dir, "sub", "b.txt"), 0o600)
		write(t, filepath.Join(dir, "ro.txt"), 0o444)
		fx.Mkdir(t, filepath.Join(dir, "empty"))
	}
	fx.MakeLongTree = func(t *testing.T, dir string) {
		t.Helper()
		fx.MakeTree(t, dir)
		deep := filepath.Join(dir, strings.Repeat("d", 90), strings.Repeat("e", 90), strings.Repeat("f", 90), "x.txt")
		write(t, deep, 0o444)
	}
	fx.Exists = func(p string) bool {
		_, err := os.Lstat(extended(p))
		return err == nil
	}
	fx.Lock = osLock
	fx.Link = osLink
	fx.Short = osShort
	return fx
}

// osLink makes a symlink on Unix and a junction on Windows (no privilege).
func osLink(t *testing.T, link, target string) bool {
	t.Helper()
	if runtime.GOOS != "windows" {
		if err := os.Symlink(target, link); err != nil {
			t.Logf("symlink: %v", err)
			return false
		}
		return true
	}
	var out bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv:   []string{filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "mklink", "/J", link, target},
		Env:    []string{"SystemRoot=" + os.Getenv("SystemRoot"), "PATH=" + os.Getenv("PATH")},
		Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Logf("start mklink: %v", err)
		return false
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Logf("mklink /J failed (exit %d, %v): %s", code, err, out.String())
		return false
	}
	return true
}
