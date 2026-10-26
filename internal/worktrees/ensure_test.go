package worktrees_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
	"github.com/brendatamaar/yggdrasil/internal/worktrees/worktreestest"
)

var ids int

func newID(prefix string) string { ids++; return fmt.Sprintf("%s_%d", prefix, ids) }

func TestWorktreesContractFake(t *testing.T) {
	worktreestest.WorktreesContract(t, func(t *testing.T, dataDir string) worktreestest.Env {
		g, s := worktreestest.NewGit(), worktreestest.NewStore()
		e, err := worktrees.New(worktrees.Config{Git: g, Store: s, Rand: &worktreestest.Rand{}, Now: time.Now, NewID: newID, DataDir: dataDir})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		return worktreestest.Env{
			Ensurer: e,
			NewRepos: func(c int) []worktrees.RepoRef {
				var out []worktrees.RepoRef
				for range c {
					n++
					out = append(out, worktrees.RepoRef{ID: core.RepoID(fmt.Sprintf("rep_%d", n)), Path: filepath.Join(t.TempDir(), "my repo"), DefaultBranch: "main"})
				}
				return out
			},
			BadRepo: func() worktrees.RepoRef {
				p := filepath.Join(t.TempDir(), "bad")
				g.MarkBad(p)
				return worktrees.RepoRef{ID: "rep_bad", Path: p}
			},
			AddBranch: func(r worktrees.RepoRef, b string) { g.AddBranch(r.Path, b) },
			HasBranch: func(r worktrees.RepoRef, b string) bool { return g.HasBranch(r.Path, b) },
			BaseOf:    func(w core.Worktree) string { return s.Base(w.ID) },
			WantBase:  func(worktrees.RepoRef) string { return worktreestest.FakeCommit },
			Audits:    func() int { return len(s.Audits) },
		}
	})
}

// realFx drives real git in temp dirs, with the user's git config replaced.
type realFx struct {
	t   *testing.T
	env []string
	g   *git.Git
	exe string
}

func newRealFx(t *testing.T) *realFx {
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
	return &realFx{t: t, env: env, g: g, exe: exe}
}

func (f *realFx) git(dir string, args ...string) string {
	f.t.Helper()
	var out, errb bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{Argv: append([]string{f.exe}, args...), Dir: dir, Env: f.env, Stdout: &out, Stderr: &errb})
	if err != nil {
		f.t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		f.t.Fatalf("git %v: exit %d, %v: %s", args, code, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func (f *realFx) repo(name string) string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-q", "-m", "init")
	return dir
}

func TestWorktreesContractRealGit(t *testing.T) {
	f := newRealFx(t)
	worktreestest.WorktreesContract(t, func(t *testing.T, dataDir string) worktreestest.Env {
		s := worktreestest.NewStore()
		e, err := worktrees.New(worktrees.Config{Git: f.g, Store: s, Rand: &worktreestest.Rand{}, Now: time.Now, NewID: newID, DataDir: dataDir})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		return worktreestest.Env{
			Ensurer: e,
			NewRepos: func(c int) []worktrees.RepoRef {
				var out []worktrees.RepoRef
				for range c {
					n++
					out = append(out, worktrees.RepoRef{ID: core.RepoID(fmt.Sprintf("rep_%d", n)), Path: f.repo(fmt.Sprintf("my repo %d", n)), DefaultBranch: "main"})
				}
				return out
			},
			BadRepo: func() worktrees.RepoRef {
				p := filepath.Join(t.TempDir(), "not a repo")
				if err := os.MkdirAll(p, 0o750); err != nil {
					t.Fatal(err)
				}
				return worktrees.RepoRef{ID: "rep_bad", Path: p, DefaultBranch: "main"}
			},
			AddBranch: func(r worktrees.RepoRef, b string) { f.git(r.Path, "branch", b) },
			HasBranch: func(r worktrees.RepoRef, b string) bool {
				local, _, err := f.g.BranchExists(context.Background(), r.Path, b)
				return err == nil && local
			},
			BaseOf:   func(w core.Worktree) string { return s.Base(w.ID) },
			WantBase: func(r worktrees.RepoRef) string { return f.git(r.Path, "rev-parse", "main") },
			Audits:   func() int { return len(s.Audits) },
		}
	})
}

func TestNewRejectsMissingParts(t *testing.T) {
	if _, err := worktrees.New(worktrees.Config{}); err == nil {
		t.Fatal("want an error for an empty config")
	}
}
