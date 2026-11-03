package proposals

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/git/gittest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// runLandingReaderContract states what the merge and removal executors rely on
// from a LandingReader (Status, WorktreeList). Run it against every
// implementation. Today only *git.Git exists: the landing fake (gittest) does
// not implement the reader, and the executors always get the real one; if a
// fake is added it must pass this suite too.
func runLandingReaderContract(t *testing.T, newWorld func(t *testing.T) (LandingReader, gittest.LandingHarness, *git.Git)) {
	ctx := context.Background()
	counts := func(st git.Status) (clean bool) {
		// The executors' definition of clean (exec_merge observeOnce).
		return st.Staged+st.Unstaged+st.Untracked+st.Conflicted == 0
	}

	t.Run("a fresh checkout is clean and reports its branch and head", func(t *testing.T) {
		r, h, _ := newWorld(t)
		st, err := r.Status(ctx, h.Dir())
		if err != nil || !counts(st) || st.Detached || st.Branch != "main" || len(st.OID) < 40 {
			t.Fatalf("Status = %+v, %v", st, err)
		}
	})
	t.Run("an untracked file makes the checkout not clean", func(t *testing.T) {
		r, h, _ := newWorld(t)
		h.WriteFile(h.Dir(), "notes/todo.txt", "x\n")
		st, err := r.Status(ctx, h.Dir())
		if err != nil || st.Untracked != 1 || counts(st) {
			t.Fatalf("Status = %+v, %v; an untracked file must count (the merge needs a clean main)", st, err)
		}
	})
	t.Run("a modified tracked file is unstaged, a staged one is staged", func(t *testing.T) {
		r, h, g := newWorld(t)
		h.CommitMain("a.txt", "one\n")
		h.WriteFile(h.Dir(), "a.txt", "two\n")
		st, _ := r.Status(ctx, h.Dir())
		if st.Unstaged != 1 || counts(st) {
			t.Fatalf("unstaged: %+v", st)
		}
		if err := g.Add(ctx, h.Dir(), []string{"a.txt"}); err != nil {
			t.Fatal(err)
		}
		st, _ = r.Status(ctx, h.Dir())
		if st.Staged != 1 || counts(st) {
			t.Fatalf("staged: %+v", st)
		}
	})
	t.Run("a worktree is read in its own folder, on its own branch", func(t *testing.T) {
		r, h, _ := newWorld(t)
		dir, base := h.Worktree("cart")
		st, err := r.Status(ctx, dir)
		if err != nil || st.Branch == "main" || st.Branch == "" || st.OID != base || !counts(st) {
			t.Fatalf("worktree status = %+v, %v (base %s)", st, err, base)
		}
		h.WriteFile(dir, "cart.txt", "x\n")
		if st, _ = r.Status(ctx, dir); counts(st) {
			t.Fatal("an untracked file in the worktree left it clean")
		}
	})
	t.Run("a conflicted merge shows as conflicted", func(t *testing.T) {
		r, h, g := newWorld(t)
		h.CommitMain("f.txt", "base\n")
		tip := h.CommitOn("feature", "f.txt", "theirs\n")
		h.CommitMain("f.txt", "ours\n")
		if _, err := g.MergeNoFF(ctx, h.Dir(), tip, "merge feature"); err == nil {
			t.Fatal("the merge should have conflicted")
		}
		st, err := r.Status(ctx, h.Dir())
		if err != nil || st.Conflicted == 0 || counts(st) {
			t.Fatalf("Status = %+v, %v", st, err)
		}
		if on, _, err := g.MergeInProgress(ctx, h.Dir()); err != nil || !on {
			t.Fatalf("MergeInProgress = %v, %v", on, err)
		}
		if err := g.MergeAbort(ctx, h.Dir()); err != nil {
			t.Fatal(err)
		}
		if st, _ = r.Status(ctx, h.Dir()); !counts(st) {
			t.Fatalf("after the abort: %+v", st)
		}
	})
	t.Run("the list has the main checkout first and the linked worktrees", func(t *testing.T) {
		r, h, _ := newWorld(t)
		dir, _ := h.Worktree("cart")
		list, err := r.WorktreeList(ctx, h.Dir())
		if err != nil || len(list) != 2 {
			t.Fatalf("list = %+v, %v", list, err)
		}
		if !samePath(list[0].Path, h.Dir()) || list[0].Branch != "main" || list[0].Prunable {
			t.Errorf("first entry = %+v, want the main checkout", list[0])
		}
		if !samePath(list[1].Path, dir) || list[1].Branch == "" || list[1].Prunable {
			t.Errorf("second entry = %+v, want %s", list[1], dir)
		}
	})
	t.Run("a worktree whose folder is gone is listed as prunable", func(t *testing.T) {
		r, h, _ := newWorld(t)
		dir, _ := h.Worktree("cart")
		if err := os.RemoveAll(dir); err != nil { // the test's own temp folder
			t.Fatal(err)
		}
		list, err := r.WorktreeList(ctx, h.Dir())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, w := range list {
			if samePath(w.Path, dir) {
				found = true
				if !w.Prunable {
					t.Errorf("entry %+v for a vanished folder is not prunable", w)
				}
			}
		}
		if !found {
			t.Errorf("the vanished worktree is not listed: %+v", list)
		}
	})
	t.Run("an unknown folder is an error, not a clean status", func(t *testing.T) {
		r, h, _ := newWorld(t)
		if st, err := r.Status(ctx, filepath.Join(h.Dir(), "no such folder")); err == nil {
			t.Fatalf("Status of a missing folder = %+v, nil", st)
		}
	})
}

// samePath compares the last element: git and the harness may spell the temp
// folder differently (8.3 short names on Windows), but the folder names are
// unique within a test.
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Base(filepath.FromSlash(a)), filepath.Base(b))
}

func TestLandingReaderContractRealGit(t *testing.T) {
	runLandingReaderContract(t, func(t *testing.T) (LandingReader, gittest.LandingHarness, *git.Git) {
		_, h := gittest.NewRealLandingWorld(t)
		gcfg := filepath.Join(t.TempDir(), "gitconfig")
		if err := os.WriteFile(gcfg, []byte("[user]\n\tname = Ada Test\n\temail = ada@example.test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{"GIT_CONFIG_GLOBAL": gcfg, "GIT_CONFIG_NOSYSTEM": "1"}})
		if err != nil {
			t.Fatal(err)
		}
		g, err := git.New(env)
		if err != nil {
			t.Skip("git is not installed")
		}
		return g, h, g
	})
}
