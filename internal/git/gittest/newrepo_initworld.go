package gittest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// FakeInitWorld is the InitWorld over the in-memory fakes.
type FakeInitWorld struct {
	Init   *FakeRepoInitializer
	Origin *FakeOriginManager
	n      int
}

var _ InitWorld = (*FakeInitWorld)(nil)

// NewFakeInitWorld is the factory for the contract suites over the fakes.
func NewFakeInitWorld(t *testing.T) InitWorld {
	t.Helper()
	return &FakeInitWorld{Init: NewFakeRepoInitializer(), Origin: NewFakeOriginManager()}
}

func (w *FakeInitWorld) NewDir() string {
	w.n++
	dir := filepath.Join("fake root", fmt.Sprintf("acme shop %d", w.n))
	w.Init.AddDir(dir)
	return dir
}
func (w *FakeInitWorld) WriteFile(dir, rel, content string) { w.Init.WriteFile(dir, rel, content) }
func (w *FakeInitWorld) SetAuthor(known bool)               { w.Init.SetAuthor(known) }
func (w *FakeInitWorld) InstallHooks(string) func() bool    { return func() bool { return false } }
func (w *FakeInitWorld) Initializer() git.RepoInitializer   { return w.Init }
func (w *FakeInitWorld) Origins() git.OriginManager         { return w.Origin }
func (w *FakeInitWorld) ReadFile(dir, rel string) (string, bool) {
	return w.Init.ReadFile(dir, rel)
}
func (w *FakeInitWorld) Staged(dir string) []string { return w.Init.Tracked(dir) }

// RealInitWorld is the InitWorld over real git in temp folders. The user's own
// git config never applies: the world owns the global config.
type RealInitWorld struct {
	t    *testing.T
	g    *git.Git
	env  []string
	exe  string
	cfg  string
	base string
	n    int
}

var _ InitWorld = (*RealInitWorld)(nil)

// NewRealInitWorld is the factory for the contract suites over real git. It
// skips the test when git is not installed.
func NewRealInitWorld(t *testing.T) InitWorld {
	t.Helper()
	base := t.TempDir()
	w := &RealInitWorld{t: t, base: base, cfg: filepath.Join(base, "gitconfig")}
	w.SetAuthor(true)
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL": w.cfg, "GIT_CONFIG_NOSYSTEM": "1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	w.env = env
	w.g, err = git.New(env)
	if errors.Is(err, git.ErrNotInstalled) {
		t.Skip("git is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	if w.exe, err = platform.Resolve("git", "", env); err != nil {
		t.Fatal(err)
	}
	return w
}

// Git returns the real wrapper (for tests that need more than the interfaces).
func (w *RealInitWorld) Git() *git.Git { return w.g }

func (w *RealInitWorld) NewDir() string {
	w.n++
	dir := filepath.Join(w.base, fmt.Sprintf("acme shop %d", w.n))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		w.t.Fatal(err)
	}
	return dir
}

func (w *RealInitWorld) WriteFile(dir, rel, content string) {
	w.t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func (w *RealInitWorld) SetAuthor(known bool) {
	text := "[user]\n\tuseConfigOnly = true\n[core]\n\tautocrlf = false\n"
	if known {
		text += "[user]\n\tname = Ada Test\n\temail = ada@example.test\n"
	}
	if err := os.WriteFile(w.cfg, []byte(text), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func (w *RealInitWorld) InstallHooks(dir string) func() bool {
	w.t.Helper()
	marker := filepath.Join(w.base, "hook-ran")
	script := "#!/bin/sh\necho ran >> \"" + filepath.ToSlash(marker) + "\"\nexit 0\n"
	hooksPathDir := filepath.Join(w.base, "my-hooks")
	for _, d := range []string{filepath.Join(dir, ".git", "hooks"), hooksPathDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			w.t.Fatal(err)
		}
		for _, name := range []string{"pre-commit", "commit-msg", "prepare-commit-msg", "post-commit", "pre-push"} {
			if err := os.WriteFile(filepath.Join(d, name), []byte(script), 0o700); err != nil { //nolint:gosec // test hook
				w.t.Fatal(err)
			}
		}
	}
	w.mustGit(dir, "config", "core.hooksPath", hooksPathDir)
	return func() bool { _, err := os.Stat(marker); return err == nil }
}

func (w *RealInitWorld) ReadFile(dir, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))) //nolint:gosec // a temp folder of the test world
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Staged lists the paths in the index (git ls-files --cached).
func (w *RealInitWorld) Staged(dir string) []string {
	w.t.Helper()
	var out, errb bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: []string{w.exe, "-c", "core.longpaths=true", "ls-files", "--cached", "-z"}, Dir: dir, Env: w.env, Stdout: &out, Stderr: &errb,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		w.t.Fatalf("git ls-files: exit %d, %v: %s", code, err, errb.String())
	}
	var names []string
	for _, n := range strings.Split(out.String(), "\x00") {
		if n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func (w *RealInitWorld) mustGit(dir string, args ...string) {
	w.t.Helper()
	var errb bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: append([]string{w.exe, "-c", "core.longpaths=true"}, args...), Dir: dir, Env: w.env, Stderr: &errb,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		w.t.Fatalf("git %v: exit %d, %v: %s", args, code, err, errb.String())
	}
}

func (w *RealInitWorld) Initializer() git.RepoInitializer { return w.g }
func (w *RealInitWorld) Origins() git.OriginManager       { return w.g }
