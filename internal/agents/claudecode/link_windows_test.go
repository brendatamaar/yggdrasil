//go:build windows

package claudecode

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

// makeDirLink creates a junction (no privilege needed) at link pointing to
// target.
func makeDirLink(t *testing.T, link, target string) {
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
}

// shortName returns the 8.3 form of an existing path, or "" if it has none.
func shortName(t *testing.T, p string) string {
	t.Helper()
	src, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 520)
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
