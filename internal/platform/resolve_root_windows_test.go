//go:build windows

package platform_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// shortName returns the 8.3 spelling of an existing path, or "" if the volume
// has no short names.
func shortName(t testing.TB, p string) string {
	t.Helper()
	src, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(src, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		return ""
	}
	s := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(s, p) {
		return ""
	}
	return s
}

// makeLink creates a junction (no privilege needed), the case that Go's
// ModeSymlink check misses.
func makeLink(t *testing.T, link, target string) bool {
	t.Helper()
	var out bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv:   []string{filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "mklink", "/J", link, target},
		Env:    []string{"SystemRoot=" + os.Getenv("SystemRoot"), "PATH=" + os.Getenv("PATH")},
		Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Fatalf("start mklink: %v", err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("mklink /J failed (exit %d, %v): %s", code, err, out.String())
	}
	return true
}

func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if len(p) >= 240 {
		p = `\\?\` + p
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

// The system temp dir is an 8.3 path on some machines (docs/spikes.md B4):
// the contract's short-name clause must really run there.
func TestShortNameClauseHasShortName(t *testing.T) {
	long := filepath.Join(canonDir(t), "A Long Directory Name")
	mkdirAll(t, long)
	if shortName(t, long) == "" {
		t.Skip("8.3 names are disabled on this volume")
	}
}
