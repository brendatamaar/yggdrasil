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
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
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

func TestStoreContractFake(t *testing.T) {
	worktreestest.StoreContract(t, func(t *testing.T) worktreestest.StoreEnv {
		s := worktreestest.NewStore()
		n := 0
		return worktreestest.StoreEnv{
			Store: s,
			NewScope: func(*testing.T) (core.TaskID, core.RepoID) {
				n++
				return core.TaskID(fmt.Sprintf("tsk_%d", n)), core.RepoID(fmt.Sprintf("rep_%d", n))
			},
			Audits: func(subject string) int {
				c := 0
				for _, a := range s.Audits {
					if a.Subject == subject {
						c++
					}
				}
				return c
			},
		}
	})
}

func TestStoreContractDB(t *testing.T) {
	worktreestest.StoreContract(t, func(t *testing.T) worktreestest.StoreEnv {
		d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "with space", "y.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		s := db.NewStore(d, nil)
		n := 0
		return worktreestest.StoreEnv{
			Store: worktrees.DBStore{S: s},
			NewScope: func(t *testing.T) (core.TaskID, core.RepoID) {
				n++
				p := dbtest.Project(t, s, core.Project{Name: fmt.Sprintf("demo%d", n), Path: filepath.Join(t.TempDir(), "proj")})
				r := dbtest.Repo(t, s, core.Repo{ProjectID: p.ID, Name: "api", Path: filepath.Join(p.Path, "api"), DefaultBranch: "main"})
				now := time.Now().UTC()
				task := core.Task{
					ID: core.TaskID(db.NewID("tsk")), ProjectID: p.ID, Title: "t", Scope: core.ScopeRepo,
					RepoIDs: []core.RepoID{r.ID}, Mode: core.ModeCode, Agent: "mock", CreatedAt: now, UpdatedAt: now,
				}
				if err := s.Commit(context.Background(), func(tx *db.Tx) error { _, err := tx.CreateTask(context.Background(), task); return err }); err != nil {
					t.Fatal(err)
				}
				return task.ID, r.ID
			},
			Audits: func(subject string) int {
				rows, err := s.ListAudit(context.Background(), subject, 100)
				if err != nil {
					t.Fatal(err)
				}
				return len(rows)
			},
		}
	})
}

func TestEnsureRefusesReuseOfRowOutsideDataDir(t *testing.T) {
	ctx := context.Background()
	g, s := worktreestest.NewGit(), worktreestest.NewStore()
	data := filepath.Join(t.TempDir(), "data")
	e, err := worktrees.New(worktrees.Config{Git: g, Store: s, Rand: &worktreestest.Rand{}, Now: time.Now, NewID: newID, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := s.Record(ctx, core.Worktree{ID: "wt_x", RepoID: "rep_x", TaskID: "tsk_x", Name: "calm-pine", Path: outside,
		Branch: "ygg/calm-pine", Status: core.WorktreeActive, CreatedAt: time.Now().UTC()}, "abc",
		core.AuditEntry{ID: "aud_x", At: time.Now().UTC(), Actor: "system", Action: "worktree.create", Subject: "task:tsk_x"}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Ensure(ctx, "tsk_x", []worktrees.RepoRef{{ID: "rep_x", Path: filepath.Join(t.TempDir(), "r"), DefaultBranch: "main"}})
	var we *worktrees.Error
	if err == nil || len(got) != 0 || !errors.As(err, &we) || strings.Contains(we.UserMessage(), outside) {
		t.Fatalf("got %+v, %v; want a refusal without a path", got, err)
	}
	if g.Adds != 0 {
		t.Fatal("a new worktree was created instead of refusing")
	}
	// A row under the data dir is reused.
	inside := filepath.Join(data, "wt", "calm-oak")
	if err := s.Record(ctx, core.Worktree{ID: "wt_y", RepoID: "rep_y", TaskID: "tsk_y", Name: "calm-oak", Path: inside,
		Branch: "ygg/calm-oak", Status: core.WorktreeActive, CreatedAt: time.Now().UTC()}, "abc",
		core.AuditEntry{ID: "aud_y", At: time.Now().UTC(), Actor: "system", Action: "worktree.create", Subject: "task:tsk_y"}); err != nil {
		t.Fatal(err)
	}
	got, err = e.Ensure(ctx, "tsk_y", []worktrees.RepoRef{{ID: "rep_y", Path: filepath.Join(t.TempDir(), "r"), DefaultBranch: "main"}})
	if err != nil || len(got) != 1 || got[0].ID != "wt_y" {
		t.Fatalf("got %+v, %v; want the recorded row reused", got, err)
	}
}

func TestNewRejectsMissingParts(t *testing.T) {
	if _, err := worktrees.New(worktrees.Config{}); err == nil {
		t.Fatal("want an error for an empty config")
	}
}
