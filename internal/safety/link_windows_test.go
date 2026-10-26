//go:build windows

package safety_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func icacls(t *testing.T, args ...string) {
	t.Helper()
	env := []string{"SystemRoot=C:\\Windows", "PATH=C:\\Windows\\System32"}
	exe, err := platform.Resolve("icacls", "", env)
	if err != nil {
		t.Fatalf("icacls not found: %v", err)
	}
	var out bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: append([]string{exe}, args...), Env: env, Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("icacls failed (%d, %v): %s", code, err, out.String())
	}
}

// denyRead makes the file unreadable for everyone (restored on cleanup).
func denyRead(t *testing.T, file string) {
	t.Helper()
	icacls(t, file, "/deny", "*S-1-1-0:(R)")
	t.Cleanup(func() { icacls(t, file, "/remove:d", "*S-1-1-0") })
}

// mkLink makes an NTFS junction (no privilege needed). It never skips: the
// junction case must run on every Windows machine.
func mkLink(t *testing.T, link, target string) {
	t.Helper()
	env := []string{"SystemRoot=C:\\Windows", "PATH=C:\\Windows\\System32"}
	cmd, err := platform.Resolve("cmd", "", env)
	if err != nil {
		t.Fatalf("cmd.exe not found: %v", err)
	}
	var out bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: []string{cmd, "/c", "mklink", "/J", link, target}, Dir: filepath.Dir(link), Env: env, Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("mklink /J failed (%d, %v): %s", code, err, out.String())
	}
}
