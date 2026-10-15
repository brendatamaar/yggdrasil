package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNilEnvRefused(t *testing.T) {
	ctx := context.Background()
	if _, err := Start(ctx, Spec{Argv: []string{"anything"}}); !errors.Is(err, ErrNoEnv) {
		t.Errorf("Start with nil env: %v, want ErrNoEnv", err)
	}
	// An empty but non-nil environment is a real (empty) environment.
	if _, err := Start(ctx, Spec{Argv: []string{"anything"}, Env: []string{}}); errors.Is(err, ErrNoEnv) {
		t.Errorf("Start with empty env: %v, want a lookup error", err)
	}
	if err := OpenEditor(ctx, "editor", t.TempDir(), nil); !errors.Is(err, ErrNoEnv) {
		t.Errorf("OpenEditor with nil env: %v, want ErrNoEnv", err)
	}
	if err := OpenFolder(ctx, t.TempDir(), nil); !errors.Is(err, ErrNoEnv) {
		t.Errorf("OpenFolder with nil env: %v, want ErrNoEnv", err)
	}
	if _, err := Resolve("git", "", nil); !errors.Is(err, ErrNoEnv) {
		t.Errorf("Resolve with nil env: %v, want ErrNoEnv", err)
	}
}

func TestCheckPathRefusesWSL(t *testing.T) {
	for _, p := range []string{
		`\\wsl$\Ubuntu\home\dev\repo`,
		`\\WSL$\Ubuntu`,
		`\\wsl.localhost\Ubuntu\home\dev\repo`,
		`//wsl$/Ubuntu/home`,
		`\\?\UNC\wsl$\Ubuntu\home`,
		`\\wsl$`,
	} {
		err := CheckPath(p)
		if !errors.Is(err, ErrWSLPath) {
			t.Errorf("CheckPath(%q) = %v, want ErrWSLPath", p, err)
			continue
		}
		if !strings.Contains(err.Error(), "Clone the repository") {
			t.Errorf("CheckPath(%q) message does not say what to do: %v", p, err)
		}
	}
	for _, p := range []string{`C:\code\wsl$`, `C:\Users\dev\wsl.localhost`, `\\server\share\repo`, `C:\code\my repo`} {
		if err := CheckPath(p); err != nil {
			t.Errorf("CheckPath(%q) = %v, want nil", p, err)
		}
	}
	if err := CheckPath(" "); err == nil {
		t.Error("CheckPath of an empty path should fail")
	}
}

func TestShortDirName(t *testing.T) {
	long := strings.Repeat("feature-", 12)
	for _, tc := range []struct {
		in        string
		unchanged bool
	}{
		{"fix-login", true},
		{"feature/login: new?", false},
		{long, false},
		{"con", false},
		{"NUL.txt", false},
		{"name. ", false},
		{"", false},
	} {
		got := ShortDirName(tc.in, 24)
		if utf8.RuneCountInString(got) > 24 || strings.ContainsAny(got, `<>:"/\|?*`) || strings.HasSuffix(got, ".") || got == "" || isReservedName(got) {
			t.Errorf("ShortDirName(%q) = %q: invalid or too long", tc.in, got)
		}
		if tc.unchanged != (got == tc.in) {
			t.Errorf("ShortDirName(%q) = %q, unchanged = %v, want %v", tc.in, got, got == tc.in, tc.unchanged)
		}
	}
	// Different names stay different, also when they shorten to the same prefix.
	if a, b := ShortDirName(long+"a", 24), ShortDirName(long+"b", 24); a == b {
		t.Errorf("distinct long names collide: %q", a)
	}
	if a, b := ShortDirName("a/b", 24), ShortDirName("a-b", 24); a == b {
		t.Errorf("sanitised name collides with a real one: %q", a)
	}
	if got := ShortDirName(long, 24); got != ShortDirName(long, 24) {
		t.Errorf("not deterministic: %q", got)
	}
}

func TestResolveWrappers(t *testing.T) {
	project := filepath.Join(t.TempDir(), "my project")
	for _, f := range []string{mvnwFile, gradlewFile, venvPython} {
		p := filepath.Join(project, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + t.TempDir()}
	for name, want := range map[string]string{
		"mvn":     mvnwFile,
		"mvnw":    mvnwFile,
		"gradle":  gradlewFile,
		"python":  venvPython,
		"python3": venvPython,
	} {
		got, err := Resolve(name, project, env)
		if err != nil {
			t.Errorf("Resolve(%q): %v", name, err)
			continue
		}
		if wantPath := filepath.Join(project, want); got != wantPath {
			t.Errorf("Resolve(%q) = %q, want %q", name, got, wantPath)
		}
	}
}

func TestResolveNotFound(t *testing.T) {
	env := []string{"PATH=" + t.TempDir()}
	_, err := Resolve("no-such-tool", t.TempDir(), env)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("message does not say what to do: %v", err)
	}
	if _, err := Resolve("mvn", t.TempDir(), env); !errors.Is(err, ErrNotFound) {
		t.Errorf("mvn without wrapper or PATH entry: %v, want ErrNotFound", err)
	}
}
