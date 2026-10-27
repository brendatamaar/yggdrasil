package gittest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// NewRealWorld returns a world on real git: a work repo on branch
// "ygg/swift-fern", a local bare remote "origin" in a temp folder, and a global
// git config the test owns (the user's own config never applies). It skips the
// test when git is not installed.
func NewRealWorld(t *testing.T) (git.Writer, Harness) {
	t.Helper()
	base := t.TempDir()
	cfg := filepath.Join(base, "gitconfig")
	r := &realHarness{t: t, cfg: cfg}
	r.setAuthor(true)
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL": cfg, "GIT_CONFIG_NOSYSTEM": "1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	r.env = env
	g, err := git.New(env)
	if errors.Is(err, git.ErrNotInstalled) {
		t.Skip("git is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	r.exe, err = platform.Resolve("git", "", env)
	if err != nil {
		t.Fatal(err)
	}
	r.bare = filepath.Join(base, "origin.git")
	r.dir = filepath.Join(base, "work")
	r.mustGit(base, "init", "-q", "--bare", "-b", "main", r.bare)
	if err := os.MkdirAll(r.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	r.mustGit(r.dir, "init", "-q", "-b", "main")
	r.WriteFile("README.md", "hello\n")
	r.mustGit(r.dir, "add", "README.md")
	r.mustGit(r.dir, "commit", "-q", "-m", "init")
	r.mustGit(r.dir, "remote", "add", "origin", r.bare)
	r.mustGit(r.dir, "push", "-q", "origin", "main")
	r.mustGit(r.dir, "remote", "set-head", "origin", "main")
	r.mustGit(r.dir, "switch", "-q", "-c", r.Branch())
	return g, r
}

type realHarness struct {
	t         *testing.T
	env       []string
	exe       string
	cfg       string
	dir, bare string
}

func (r *realHarness) setAuthor(known bool) {
	text := "[user]\n\tuseConfigOnly = true\n"
	if known {
		text += "\tname = Ada Test\n\temail = ada@example.test\n"
	}
	if err := os.WriteFile(r.cfg, []byte(text), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *realHarness) mustGit(dir string, args ...string) string {
	r.t.Helper()
	var out, errb bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: append([]string{r.exe, "-c", "core.longpaths=true"}, args...), Dir: dir, Env: r.env, Stdout: &out, Stderr: &errb,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	code, err := p.Wait()
	if err != nil || code != 0 {
		r.t.Fatalf("git %v: exit %d, %v: %s", args, code, err, errb.String())
	}
	return out.String()
}

func (r *realHarness) Dir() string    { return r.dir }
func (r *realHarness) Branch() string { return "ygg/swift-fern" }

func (r *realHarness) WriteFile(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *realHarness) RemoteTip(branch string) string {
	out := r.mustGit(r.bare, "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
	return strings.TrimSpace(out)
}

func (r *realHarness) AdvanceRemote(branch string) {
	r.t.Helper()
	other := filepath.Join(filepath.Dir(r.bare), "other")
	r.mustGit(filepath.Dir(r.bare), "clone", "-q", r.bare, other)
	if r.RemoteTip(branch) != "" {
		r.mustGit(other, "switch", "-q", branch)
	} else {
		r.mustGit(other, "switch", "-q", "-c", branch)
	}
	if err := os.WriteFile(filepath.Join(other, "elsewhere.txt"), []byte(branch), 0o600); err != nil {
		r.t.Fatal(err)
	}
	r.mustGit(other, "add", "elsewhere.txt")
	r.mustGit(other, "commit", "-q", "-m", "elsewhere")
	r.mustGit(other, "push", "-q", "origin", branch)
}

func (r *realHarness) SetOriginURL(u string) {
	r.mustGit(r.dir, "remote", "set-url", "origin", u)
}

func (r *realHarness) InstallHooks() (func() bool, bool) {
	r.t.Helper()
	marker := filepath.Join(filepath.Dir(r.dir), "hook-ran")
	script := "#!/bin/sh\necho ran >> \"" + filepath.ToSlash(marker) + "\"\nexit 0\n"
	hooksPathDir := filepath.Join(filepath.Dir(r.dir), "my-hooks")
	for _, d := range []string{filepath.Join(r.dir, ".git", "hooks"), hooksPathDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			r.t.Fatal(err)
		}
		for _, name := range []string{"pre-commit", "commit-msg", "prepare-commit-msg", "post-commit", "pre-push"} {
			if err := os.WriteFile(filepath.Join(d, name), []byte(script), 0o700); err != nil { //nolint:gosec // test hook
				r.t.Fatal(err)
			}
		}
	}
	r.mustGit(r.dir, "config", "core.hooksPath", hooksPathDir)
	return func() bool { _, err := os.Stat(marker); return err == nil }, true
}

func (r *realHarness) UnsetAuthor() bool {
	r.setAuthor(false)
	return true
}
