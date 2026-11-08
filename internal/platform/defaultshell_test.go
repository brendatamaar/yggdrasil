//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestShellFrom(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "my shell")
	if err := os.WriteFile(good, nil, 0o755); err != nil { //nolint:gosec // a fake shell must be executable
		t.Fatal(err)
	}
	notExec := filepath.Join(dir, "plain")
	if err := os.WriteFile(notExec, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		shell string
		want  []string
	}{
		{good, []string{good}},
		{"", []string{"/bin/sh"}},
		{"zsh", []string{"/bin/sh"}}, // not absolute
		{notExec, []string{"/bin/sh"}},
		{filepath.Join(dir, "missing"), []string{"/bin/sh"}},
	} {
		if got := shellFrom(tc.shell); !slices.Equal(got, tc.want) {
			t.Errorf("shellFrom(%q) = %v, want %v", tc.shell, got, tc.want)
		}
	}
}

func TestDefaultShellFollowsSHELL(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	if got := DefaultShell(); len(got) != 1 || got[0] != "/bin/sh" {
		t.Errorf("DefaultShell = %v", got)
	}
}
