//go:build windows

package platform

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A junction is not a symlink: Go's os.ModeSymlink check misses it, the
// reparse attribute does not.
func TestIsReparsePointJunction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "junction")
	// mklink /J needs no privilege.
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
	if got, err := IsReparsePoint(link); err != nil || !got {
		t.Errorf("IsReparsePoint(junction) = %v, %v; want true", got, err)
	}
	if got, err := IsReparsePoint(target); err != nil || got {
		t.Errorf("IsReparsePoint(target) = %v, %v; want false", got, err)
	}
}
