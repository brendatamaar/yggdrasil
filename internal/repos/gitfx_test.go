package repos_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// gitFx builds temp git repos through platform.Start, like the code under
// test (exec.Command is forbidden outside internal/platform).
type gitFx struct {
	t   *testing.T
	g   *git.Git
	env []string
	exe string
}

// newGitFx skips the test when git is not installed. The user's git config is
// replaced, so a result cannot depend on it.
func newGitFx(t *testing.T) *gitFx {
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
	g, err := git.New(env)
	if errors.Is(err, git.ErrNotInstalled) {
		t.Skip("git is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		t.Fatal(err)
	}
	return &gitFx{t: t, g: g, env: env, exe: exe}
}

func (f *gitFx) run(dir string, args ...string) {
	f.t.Helper()
	if err := f.runErr(dir, args...); err != nil {
		f.t.Fatal(err)
	}
}

// runErr is run for goroutines other than the test's: it returns the error.
func (f *gitFx) runErr(dir string, args ...string) error {
	var out, errb bytes.Buffer
	argv := append([]string{f.exe, "-c", "core.longpaths=true"}, args...)
	p, err := platform.Start(context.Background(), platform.Spec{Argv: argv, Dir: dir, Env: f.env, Stdout: &out, Stderr: &errb})
	if err != nil {
		return err
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		return fmt.Errorf("git %v: exit %d, err %v: %s", args, code, err, errb.String())
	}
	return nil
}

func (f *gitFx) write(dir, name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gitFx) commit(dir, name, content, msg string) {
	f.t.Helper()
	f.write(dir, name, content)
	f.run(dir, "add", "-A")
	f.run(dir, "commit", "-q", "-m", msg)
}

// repo makes an empty repo on branch main.
func (f *gitFx) repo(name string) string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), name)
	f.makeEmpty(dir)
	return dir
}

// makeEmpty creates dir and runs git init -b main in it.
func (f *gitFx) makeEmpty(dir string) {
	f.t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		f.t.Fatal(err)
	}
	f.run(dir, "init", "-q", "-b", "main")
}

// Prepare implements repostest.StatusSetup with real repos.
func (f *gitFx) Prepare(t *testing.T, sc repostest.StatusScenario) corerepos.CleanPath {
	t.Helper()
	var dir string
	switch sc {
	case repostest.ScenarioMissing:
		dir = filepath.Join(t.TempDir(), "gone")
	case repostest.ScenarioNotARepo:
		dir = t.TempDir()
	case repostest.ScenarioNoCommits:
		dir = f.repo("fresh")
	case repostest.ScenarioAheadBehind:
		// No network and no transport: origin/main is a ref made by hand, one
		// commit away from main on a side branch.
		dir = f.repo("work")
		f.commit(dir, "a.txt", "a\n", "init")
		f.run(dir, "checkout", "-q", "-b", "up")
		f.commit(dir, "up.txt", "u\n", "upstream work")
		f.run(dir, "update-ref", "refs/remotes/origin/main", "up")
		f.run(dir, "checkout", "-q", "main")
		f.run(dir, "branch", "-D", "up")
		f.run(dir, "config", "remote.origin.url", "https://example.invalid/acme/shop-api.git")
		f.run(dir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
		f.run(dir, "config", "branch.main.remote", "origin")
		f.run(dir, "config", "branch.main.merge", "refs/heads/main")
		f.commit(dir, "local.txt", "l\n", "local work")
	default:
		dir = f.repo("work")
		f.commit(dir, "a.txt", "a\n", "init")
		switch sc {
		case repostest.ScenarioDetached:
			f.run(dir, "checkout", "-q", "--detach")
		case repostest.ScenarioDirty:
			f.write(dir, "a.txt", "changed\n") // unstaged
			f.write(dir, "staged.txt", "s\n")
			f.run(dir, "add", "staged.txt")
			f.write(dir, "untracked.txt", "u\n")
		}
	}
	p, err := corerepos.NewCleanPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
