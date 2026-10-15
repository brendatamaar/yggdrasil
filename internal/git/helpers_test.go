package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// fx builds temp repos. It starts git through platform.Start like the code
// under test; exec.Command is forbidden by lint.
type fx struct {
	t   *testing.T
	g   *Git
	env []string
	exe string
}

// newFx skips the test when git is not installed. The global and system git
// config are replaced, so the user's settings (autocrlf, hooks) cannot change
// a result.
func newFx(t *testing.T) *fx {
	t.Helper()
	t.Parallel() // each test has its own temp repos; git start-up dominates the run time
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL":   cfg,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Ada Test", "GIT_AUTHOR_EMAIL": "ada@example.test",
		"GIT_COMMITTER_NAME": "Ada Test", "GIT_COMMITTER_EMAIL": "ada@example.test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(env)
	if errors.Is(err, ErrNotInstalled) {
		t.Skip("git is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	return &fx{t: t, g: g, env: env, exe: g.exe}
}

// git runs git in dir and fails the test on a non-zero exit.
func (f *fx) git(dir string, args ...string) string {
	f.t.Helper()
	var out, errb bytes.Buffer
	argv := append([]string{f.exe, "-c", "core.longpaths=true"}, args...)
	p, err := platform.Start(context.Background(), platform.Spec{Argv: argv, Dir: dir, Env: f.env, Stdout: &out, Stderr: &errb})
	if err != nil {
		f.t.Fatal(err)
	}
	code, err := p.Wait()
	if err != nil || code != 0 {
		f.t.Fatalf("git %v: exit %d, err %v: %s", args, code, err, errb.String())
	}
	return out.String()
}

// repo creates an empty repo on branch main in a new temp dir.
func (f *fx) repo(name string) string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "init", "-q", "-b", "main")
	return dir
}

func (f *fx) write(dir, rel, content string) {
	f.t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) commit(dir, rel, content, msg string) {
	f.t.Helper()
	f.write(dir, rel, content)
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-q", "-m", msg)
}

// pair returns a work repo "a" and a clone "b" of the same bare remote, both
// on main, in sync, with b tracking origin/main.
func (f *fx) pair() (a, b string) {
	f.t.Helper()
	base := f.t.TempDir()
	bare := filepath.Join(base, "origin.git")
	f.git(base, "init", "-q", "--bare", "-b", "main", bare)
	a = f.repo("a")
	f.commit(a, "README.md", "hello\n", "init")
	f.git(a, "remote", "add", "origin", bare)
	f.git(a, "push", "-q", "-u", "origin", "main")
	b = filepath.Join(base, "b")
	f.git(base, "clone", "-q", bare, b)
	return a, b
}

func bg() context.Context { return context.Background() }
