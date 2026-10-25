package searchtest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// GitEnv returns a clean environment whose git config is empty, so the user's
// settings cannot change a result. The test is skipped when git is missing.
func GitEnv(t *testing.T) []string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL": cfg, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Ada Test", "GIT_AUTHOR_EMAIL": "ada@example.test",
		"GIT_COMMITTER_NAME": "Ada Test", "GIT_COMMITTER_EMAIL": "ada@example.test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Resolve("git", "", env); err != nil {
		t.Skip("git is not installed")
	}
	return env
}

// InitRepo creates a temp git repo holding files (path to content, "/"
// separators), all committed, and returns its folder. Git runs through
// platform.Start like the code under test.
func InitRepo(t *testing.T, env []string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	RunGit(t, env, dir, "init", "-q")
	RunGit(t, env, dir, "add", "-A")
	RunGit(t, env, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	return dir
}

// GitOutput runs git in dir with stdin and returns its trimmed stdout. The
// test fails on a non-zero exit.
func GitOutput(t *testing.T, env []string, dir, stdin string, args ...string) string {
	t.Helper()
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: append([]string{exe}, args...), Dir: dir, Env: env,
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("git %v: exit %d, %v\n%s", args, code, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// RunGit runs git in dir and fails the test on a non-zero exit.
func RunGit(t *testing.T, env []string, dir string, args ...string) {
	t.Helper()
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: append([]string{exe}, args...), Dir: dir, Env: env, Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("git %v: exit %d, %v\n%s", args, code, err, out.String())
	}
}
