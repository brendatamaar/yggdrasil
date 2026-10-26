package worktreestest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
)

// Ensurer is what the contract tests.
type Ensurer interface {
	Ensure(ctx context.Context, task core.TaskID, repos []worktrees.RepoRef) ([]core.Worktree, error)
}

// Env is one implementation under test. The Ensurer must use a fresh Rand
// (see Rand) and the DataDir the factory was given.
type Env struct {
	Ensurer Ensurer
	// NewRepos makes n tracked repos with a commit on their default branch.
	NewRepos func(n int) []worktrees.RepoRef
	// BadRepo makes a repo that git cannot work in.
	BadRepo func() worktrees.RepoRef
	// AddBranch makes the branch exist in the repo.
	AddBranch func(repo worktrees.RepoRef, branch string)
	// HasBranch reports whether the branch exists in the repo.
	HasBranch func(repo worktrees.RepoRef, branch string) bool
	// BaseOf returns the recorded base commit of a worktree.
	BaseOf func(w core.Worktree) string
	// WantBase returns the commit the repo's default branch points to.
	WantBase func(repo worktrees.RepoRef) string
	// Audits returns how many audit entries were written.
	Audits func() int
}

// Factory builds an Env whose worktrees go under dataDir.
type Factory func(t *testing.T, dataDir string) Env

// WorktreesContract runs the contract of worktrees.Ensurer.
func WorktreesContract(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()
	newEnv := func(t *testing.T) (Env, string) {
		dir := filepath.Join(t.TempDir(), "data dir")
		return f(t, dir), dir
	}

	t.Run("creates one per repo under the data dir", func(t *testing.T) {
		env, dir := newEnv(t)
		repos := env.NewRepos(2)
		got, err := env.Ensurer.Ensure(ctx, "tsk_1", repos)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d worktrees, want 2", len(got))
		}
		root, err := platform.LongPath(filepath.Join(dir, "wt"))
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for i, w := range got {
			if w.RepoID != repos[i].ID || w.TaskID != "tsk_1" || w.Status != core.WorktreeActive {
				t.Errorf("worktree %d has wrong identity: %+v", i, w)
			}
			if filepath.Dir(w.Path) != root {
				t.Errorf("worktree %d is at %q, want a folder directly under %q", i, w.Path, root)
			}
			if seen[w.Path] || seen[w.Name] {
				t.Errorf("worktree %d reuses a name or folder", i)
			}
			seen[w.Path], seen[w.Name] = true, true
			if _, err := os.Stat(w.Path); err != nil {
				t.Errorf("worktree %d folder is missing: %v", i, err)
			}
			if _, err := worktrees.ParseName(w.Name); err != nil {
				t.Errorf("name %q is not a valid name: %v", w.Name, err)
			}
			if w.Branch != "ygg/"+w.Name || !env.HasBranch(repos[i], w.Branch) {
				t.Errorf("branch %q is not the new branch ygg/%s", w.Branch, w.Name)
			}
			if b := env.BaseOf(w); b == "" || b != env.WantBase(repos[i]) {
				t.Errorf("base commit %q, want %q", b, env.WantBase(repos[i]))
			}
		}
		if n := env.Audits(); n != 2 {
			t.Errorf("%d audit entries, want 2", n)
		}
	})

	t.Run("path is short and in long form", func(t *testing.T) {
		env, _ := newEnv(t)
		got, err := env.Ensurer.Ensure(ctx, "tsk_1", env.NewRepos(1))
		if err != nil {
			t.Fatal(err)
		}
		p := got[0].Path
		if n := len(filepath.Base(p)); n > worktrees.MaxNameLen {
			t.Errorf("folder name %q has %d characters, max %d", filepath.Base(p), n, worktrees.MaxNameLen)
		}
		long, err := platform.LongPath(p)
		if err != nil || long != p {
			t.Errorf("path %q is not in long form (%q, %v)", p, long, err)
		}
	})

	t.Run("second call reuses", func(t *testing.T) {
		env, _ := newEnv(t)
		repos := env.NewRepos(2)
		first, err := env.Ensurer.Ensure(ctx, "tsk_1", repos[:1])
		if err != nil {
			t.Fatal(err)
		}
		both, err := env.Ensurer.Ensure(ctx, "tsk_1", repos)
		if err != nil {
			t.Fatal(err)
		}
		if len(both) != 2 || both[0].ID != first[0].ID || both[0].Path != first[0].Path {
			t.Fatalf("first worktree was not reused: %+v then %+v", first, both)
		}
		again, err := env.Ensurer.Ensure(ctx, "tsk_1", repos)
		if err != nil || again[0].ID != both[0].ID || again[1].ID != both[1].ID {
			t.Fatalf("third call changed worktrees: %+v, %v", again, err)
		}
		if n := env.Audits(); n != 2 {
			t.Errorf("%d audit entries after reuse, want 2", n)
		}
	})

	t.Run("another task gets its own", func(t *testing.T) {
		env, _ := newEnv(t)
		repos := env.NewRepos(1)
		a, err := env.Ensurer.Ensure(ctx, "tsk_1", repos)
		if err != nil {
			t.Fatal(err)
		}
		b, err := env.Ensurer.Ensure(ctx, "tsk_2", repos)
		if err != nil {
			t.Fatal(err)
		}
		if a[0].ID == b[0].ID || a[0].Path == b[0].Path || a[0].Branch == b[0].Branch {
			t.Fatalf("two tasks share a worktree: %+v %+v", a[0], b[0])
		}
	})

	t.Run("name collision retries with a new name", func(t *testing.T) {
		env, _ := newEnv(t)
		repo := env.NewRepos(1)[0]
		taken := worktrees.Generate(&Rand{}, 0)
		env.AddBranch(repo, taken.Branch())
		got, err := env.Ensurer.Ensure(ctx, "tsk_1", []worktrees.RepoRef{repo})
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Name == string(taken) || got[0].Branch == taken.Branch() {
			t.Fatalf("worktree took the name that was already used: %+v", got[0])
		}
	})

	t.Run("a folder that exists counts as a collision", func(t *testing.T) {
		env, dir := newEnv(t)
		repo := env.NewRepos(1)[0]
		taken := worktrees.Generate(&Rand{}, 0)
		if err := os.MkdirAll(filepath.Join(dir, "wt", string(taken)), 0o750); err != nil {
			t.Fatal(err)
		}
		got, err := env.Ensurer.Ensure(ctx, "tsk_1", []worktrees.RepoRef{repo})
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Name == string(taken) {
			t.Fatalf("worktree reused an existing folder: %+v", got[0])
		}
		if _, err := os.Stat(filepath.Join(dir, "wt", string(taken))); err != nil {
			t.Errorf("the existing folder was touched: %v", err)
		}
	})

	t.Run("gives up after five taken names", func(t *testing.T) {
		env, _ := newEnv(t)
		repo := env.NewRepos(1)[0]
		r := &Rand{}
		for i := range 5 {
			env.AddBranch(repo, worktrees.Generate(r, i).Branch())
		}
		got, err := env.Ensurer.Ensure(ctx, "tsk_1", []worktrees.RepoRef{repo})
		if !errors.Is(err, worktrees.ErrNameTaken) || len(got) != 0 {
			t.Fatalf("got %+v, %v; want ErrNameTaken and nothing created", got, err)
		}
	})

	t.Run("partial failure keeps earlier worktrees", func(t *testing.T) {
		env, dir := newEnv(t)
		good := env.NewRepos(2)
		bad := env.BadRepo()
		got, err := env.Ensurer.Ensure(ctx, "tsk_1", []worktrees.RepoRef{good[0], bad, good[1]})
		if err == nil {
			t.Fatal("want an error for the broken repo")
		}
		var we *worktrees.Error
		if !errors.As(err, &we) || we.UserMessage() == "" {
			t.Fatalf("error %v has no plain-language message", err)
		}
		if strings.Contains(we.UserMessage(), dir) || strings.Contains(we.UserMessage(), bad.Path) {
			t.Errorf("user message holds a path: %q", we.UserMessage())
		}
		if len(got) != 1 {
			t.Fatalf("got %d worktrees, want the 1 created before the failure", len(got))
		}
		if _, err := os.Stat(got[0].Path); err != nil {
			t.Errorf("earlier worktree was removed: %v", err)
		}
		again, err := env.Ensurer.Ensure(ctx, "tsk_1", good)
		if err != nil || len(again) != 2 || again[0].ID != got[0].ID {
			t.Fatalf("retry did not reuse the earlier worktree: %+v, %v", again, err)
		}
	})

	t.Run("refuses wsl paths", func(t *testing.T) {
		env, _ := newEnv(t)
		got, err := env.Ensurer.Ensure(ctx, "tsk_1", []worktrees.RepoRef{{ID: "rep_w", Path: `\\wsl$\Ubuntu\home\ada\shop-api`}})
		if err == nil || len(got) != 0 {
			t.Fatalf("got %+v, %v; want a refusal", got, err)
		}
	})

	t.Run("works with a long data dir", func(t *testing.T) {
		long := t.TempDir()
		for len(long) < 150 {
			long = filepath.Join(long, "long-folder-name-x")
		}
		env := f(t, filepath.Join(long, "data"))
		got, err := env.Ensurer.Ensure(ctx, "tsk_1", env.NewRepos(1))
		if err != nil {
			t.Fatalf("long data dir: %v", err)
		}
		if _, err := os.Stat(got[0].Path); err != nil {
			t.Errorf("worktree missing: %v", err)
		}
	})
}
