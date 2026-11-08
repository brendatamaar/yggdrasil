package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeExe creates an executable stand-in called name in dir.
func fakeExe(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o755); err != nil { //nolint:gosec // a fake executable must be executable
		t.Fatal(err)
	}
	return p
}

func TestPythonBaseUsesPathOfEnv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "my tools")
	want := fakeExe(t, bin, "python3")
	got, err := PythonBase([]string{"PATH=" + bin})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPythonBaseFallsBackToPython(t *testing.T) {
	bin := t.TempDir()
	want := fakeExe(t, bin, "python")
	got, err := PythonBase([]string{"PATH=" + bin})
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

// A venv named by VIRTUAL_ENV is never the base of a new venv.
func TestPythonBaseIgnoresVirtualEnv(t *testing.T) {
	venv := t.TempDir()
	venvBin := filepath.Join(venv, "bin")
	fakeExe(t, venvBin, "python3")
	fakeExe(t, filepath.Join(venv, "Scripts"), "python3")
	system := filepath.Join(t.TempDir(), "system")
	want := fakeExe(t, system, "python3")

	got, err := PythonBase([]string{"VIRTUAL_ENV=" + venv, "PATH=" + system})
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
	// With nothing on the PATH it fails instead of using the venv.
	_, err = PythonBase([]string{"VIRTUAL_ENV=" + venv, "PATH=" + t.TempDir()})
	if !errors.Is(err, ErrNoPython) {
		t.Fatalf("got %v, want ErrNoPython", err)
	}
}

func TestPythonBaseErrors(t *testing.T) {
	if _, err := PythonBase(nil); !errors.Is(err, ErrNoEnv) {
		t.Errorf("nil env: %v, want ErrNoEnv", err)
	}
	_, err := PythonBase([]string{"PATH=" + t.TempDir()})
	if !errors.Is(err, ErrNoPython) {
		t.Fatalf("got %v, want ErrNoPython", err)
	}
	if !strings.Contains(err.Error(), "Install Python") {
		t.Errorf("message does not say what to do: %v", err)
	}
}
