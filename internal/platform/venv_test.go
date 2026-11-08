package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveVenvPython(t *testing.T) {
	env := []string{"PATH=" + t.TempDir()}
	for _, dirName := range []string{"my project", "plain", "C-drive like dir"} {
		project := filepath.Join(t.TempDir(), dirName)
		want := filepath.Join(project, venvPython)
		if err := os.MkdirAll(filepath.Dir(want), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(want, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".venv/python", `.venv\python`} {
			got, err := Resolve(name, project, env)
			if err != nil {
				t.Fatalf("Resolve(%q) in %q: %v", name, project, err)
			}
			if got != want {
				t.Errorf("Resolve(%q) = %q, want %q", name, got, want)
			}
		}
	}
}

func TestResolveVenvMissing(t *testing.T) {
	env := []string{"PATH=" + t.TempDir()}
	_, err := Resolve(".venv/python", t.TempDir(), env)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "install") {
		t.Errorf("message does not say what to do: %v", err)
	}
	if _, err := Resolve(".venv/python", "", env); !errors.Is(err, ErrNotFound) {
		t.Errorf("no dir: %v, want ErrNotFound", err)
	}
	if _, err := Resolve(".venv/python", t.TempDir(), nil); !errors.Is(err, ErrNoEnv) {
		t.Errorf("nil env: %v, want ErrNoEnv", err)
	}
}

func TestResolvePythonBase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-style fake interpreter")
	}
	bin := t.TempDir()
	want := filepath.Join(bin, "python3")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // test fake must be executable
		t.Fatal(err)
	}
	// A venv named by VIRTUAL_ENV must never be used.
	venv := t.TempDir()
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "bin", "python3"), []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // test fake
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin, "VIRTUAL_ENV=" + venv}
	got, err := Resolve("python-base", t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
