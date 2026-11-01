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

// NewFakeLandingWorld returns a landing world on the in-memory fake.
func NewFakeLandingWorld(t *testing.T) (LandingWorld, LandingHarness) {
	const dir = "/fake/shop-api"
	f := NewFakeLanding(dir)
	return f, &fakeLandingHarness{f: f, dir: dir}
}

type fakeLandingHarness struct {
	f   *FakeLanding
	dir string
}

func (h *fakeLandingHarness) Dir() string { return h.dir }

func (h *fakeLandingHarness) CommitOn(branch, rel, content string) string {
	return h.f.CommitOnBranch(branch, rel, content)
}

func (h *fakeLandingHarness) CommitMain(rel, content string) string {
	h.f.WriteFile(h.dir, rel, content)
	return h.f.CommitAll(h.dir, "commit "+rel)
}

func (h *fakeLandingHarness) Worktree(name string) (string, string) {
	dir := "/fake/wt/" + name
	h.f.AddWorktree(dir, "ygg/"+name)
	base, _ := h.f.BranchHead(context.Background(), dir, "main")
	return dir, base
}

func (h *fakeLandingHarness) MoveFile(dir, from, to string) { h.f.MoveFile(dir, from, to) }
func (h *fakeLandingHarness) WriteFile(dir, rel, content string) {
	h.f.WriteFile(dir, rel, content)
}
func (h *fakeLandingHarness) ReadFile(dir, rel string) (string, bool) { return h.f.ReadFile(dir, rel) }
func (h *fakeLandingHarness) CommitAll(dir, msg string) string        { return h.f.CommitAll(dir, msg) }
func (h *fakeLandingHarness) TreeUnchanged(dir string) bool           { return h.f.TreeUnchanged(dir) }
func (h *fakeLandingHarness) Parents(c string) []string               { return h.f.Parents(c) }
func (h *fakeLandingHarness) RemoteRefs() map[string]string           { return map[string]string{} }
func (h *fakeLandingHarness) UnsetIdentity() bool                     { h.f.SetNoIdentity(true); return true }

// NewRealLandingWorld returns a landing world on real git: a work repo on
// "main" with a local bare remote "origin" and a global git config the test
// owns (the user's own config never applies; no autocrlf). It skips the test
// when git is not installed.
func NewRealLandingWorld(t *testing.T) (LandingWorld, LandingHarness) {
	t.Helper()
	base := t.TempDir()
	r := &realLanding{t: t, base: base, cfg: filepath.Join(base, "gitconfig")}
	r.setIdentity(true)
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL": r.cfg, "GIT_CONFIG_NOSYSTEM": "1",
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
	if r.exe, err = platform.Resolve("git", "", env); err != nil {
		t.Fatal(err)
	}
	r.bare = filepath.Join(base, "origin.git")
	r.dir = filepath.Join(base, "work")
	r.mustGit(base, "init", "-q", "--bare", "-b", "main", r.bare)
	if err := os.MkdirAll(r.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	r.mustGit(r.dir, "init", "-q", "-b", "main")
	r.WriteFile(r.dir, "README.md", "hello\n")
	r.CommitAll(r.dir, "init")
	r.mustGit(r.dir, "remote", "add", "origin", r.bare)
	r.mustGit(r.dir, "push", "-q", "origin", "main")
	return struct {
		git.LandingGit
		git.BaseDiffer
	}{g, g}, r
}

type realLanding struct {
	t         *testing.T
	env       []string
	exe, cfg  string
	base      string
	dir, bare string
}

func (r *realLanding) setIdentity(known bool) {
	text := "[user]\n\tuseConfigOnly = true\n"
	if known {
		text += "\tname = Ada Test\n\temail = ada@example.test\n"
	}
	if err := os.WriteFile(r.cfg, []byte(text), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// gitCode runs git and returns stdout and the exit code.
func (r *realLanding) gitCode(dir string, args ...string) (string, int) {
	r.t.Helper()
	var out, errb bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: append([]string{r.exe, "-c", "core.longpaths=true", "-c", "core.quotepath=false"}, args...), Dir: dir, Env: r.env, Stdout: &out, Stderr: &errb,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	code, err := p.Wait()
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, errb.String())
	}
	return out.String(), code
}

func (r *realLanding) mustGit(dir string, args ...string) string {
	r.t.Helper()
	out, code := r.gitCode(dir, args...)
	if code != 0 {
		r.t.Fatalf("git %v: exit %d", args, code)
	}
	return out
}

func (r *realLanding) Dir() string { return r.dir }

func (r *realLanding) CommitOn(branch, rel, content string) string {
	r.t.Helper()
	if _, code := r.gitCode(r.dir, "rev-parse", "-q", "--verify", "refs/heads/"+branch); code == 0 {
		r.mustGit(r.dir, "switch", "-q", branch)
	} else {
		r.mustGit(r.dir, "switch", "-q", "-c", branch, "main")
	}
	r.WriteFile(r.dir, rel, content)
	tip := r.CommitAll(r.dir, "commit "+rel)
	r.mustGit(r.dir, "switch", "-q", "main")
	return tip
}

func (r *realLanding) CommitMain(rel, content string) string {
	r.t.Helper()
	r.WriteFile(r.dir, rel, content)
	return r.CommitAll(r.dir, "commit "+rel)
}

func (r *realLanding) Worktree(name string) (string, string) {
	r.t.Helper()
	dir := filepath.Join(r.base, "wt-"+name)
	r.mustGit(r.dir, "worktree", "add", "-q", "-b", "ygg/"+name, dir, "main")
	return dir, strings.TrimSpace(r.mustGit(r.dir, "rev-parse", "main"))
}

func (r *realLanding) WriteFile(dir, rel, content string) {
	r.t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *realLanding) MoveFile(dir, from, to string) {
	r.t.Helper()
	dst := filepath.Join(dir, filepath.FromSlash(to))
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, filepath.FromSlash(from)), dst); err != nil {
		r.t.Fatal(err)
	}
}

func (r *realLanding) ReadFile(dir, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))) //nolint:gosec // test helper reads a temp repo
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (r *realLanding) CommitAll(dir, msg string) string {
	r.t.Helper()
	r.mustGit(dir, "add", "-A")
	r.mustGit(dir, "commit", "-q", "-m", msg)
	return strings.TrimSpace(r.mustGit(dir, "rev-parse", "HEAD"))
}

func (r *realLanding) TreeUnchanged(dir string) bool {
	r.t.Helper()
	if _, code := r.gitCode(dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); code == 0 {
		return false
	}
	return strings.TrimSpace(r.mustGit(dir, "status", "--porcelain=v1", "--untracked-files=all")) == ""
}

func (r *realLanding) Parents(commit string) []string {
	f := strings.Fields(r.mustGit(r.dir, "rev-list", "--parents", "-n", "1", commit))
	if len(f) < 2 {
		return nil
	}
	return f[1:]
}

func (r *realLanding) RemoteRefs() map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(r.mustGit(r.bare, "for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			m[k] = v
		}
	}
	return m
}

func (r *realLanding) UnsetIdentity() bool {
	r.setIdentity(false)
	return true
}
