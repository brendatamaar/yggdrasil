package git

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Invariant 3: the git processes that verification, bootstrap and landing start
// (merge, status, worktree list, branch head) all go through Git.run, so the
// environment of a real git child is the clean one: the host process's tokens
// are not in it, whatever the caller's own environment holds. The child prints
// its own environment (a shell alias running env), so the test sees what git
// really got, not what we meant to pass.
func TestInvariant_3_git_children_get_the_clean_env(t *testing.T) {
	secrets := map[string]string{
		"GITHUB_TOKEN":          "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"GITLAB_TOKEN":          "glpat-bbbbbbbbbbbbbbbbbbbb",
		"YGG_API_KEY":           "sk-cccccccccccccccccccc",
		"AWS_SECRET_ACCESS_KEY": "dddddddddddddddddddd",
		"NPM_TOKEN":             "eeeeeeeeeeeeeeeeeeee",
		"VIRTUAL_ENV":           "C:/venv-leak",
		"PYTHONPATH":            "C:/pp-leak",
		"CONDA_PREFIX":          "C:/conda-leak",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
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
	dir := t.TempDir()
	out, err := g.run(context.Background(), dir, "printenv", true, "-c", "alias.printenv=!env", "printenv")
	if err != nil {
		t.Skipf("this git cannot run a shell alias: %v", err)
	}
	if !strings.Contains(out, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("the child did not print its environment (or prompts are on):\n%s", out)
	}
	for k, v := range secrets {
		if strings.Contains(out, v) || strings.Contains(strings.ToUpper(out), k+"=") {
			t.Errorf("%s reached a git child", k)
		}
	}
	// And the wrapper itself keeps the guarantee for any env it is given.
	for _, kv := range New0(t, env).env {
		for _, v := range secrets {
			if strings.Contains(kv, v) {
				t.Errorf("Git.env holds %q", kv)
			}
		}
	}
}

// New0 builds a Git or fails the test.
func New0(t *testing.T, env []string) *Git {
	t.Helper()
	g, err := New(env)
	if err != nil {
		t.Skip(err)
	}
	return g
}
