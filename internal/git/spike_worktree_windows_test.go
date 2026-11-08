//go:build windows

package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// TestSpikeWorktreeRemoveWithLockedFile checks a premise of docs/design/landing.md:
// what git does when it is asked to take a worktree away while a process holds
// a file in it open (opened without share-delete, like most editors and dev
// servers). It only records facts with t.Logf; production code never calls this
// git subcommand (landing renames the folder instead, see platform.MoveDir).
// Run with -v to read the result.
func TestSpikeWorktreeRemoveWithLockedFile(t *testing.T) {
	f := newFx(t)
	d := f.repo("spike")
	f.commit(d, "held.txt", "held\n", "init")
	f.commit(d, "dir/other.txt", "other\n", "second")
	f.commit(d, "dir/deep/third.txt", "third\n", "third")
	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := f.g.WorktreeAdd(bg(), d, wt, "ygg/spike", ""); err != nil {
		t.Fatal(err)
	}

	// Go opens files with share read+write but not delete: this blocks a
	// delete or a rename of the file, and of its folders.
	held, err := os.Open(filepath.Join(wt, "dir", "deep", "third.txt"))
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = held.Close()
		}
	}()

	gitCode := func(dir string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		p, err := platform.Start(context.Background(), platform.Spec{
			Argv: append([]string{f.exe, "-c", "core.longpaths=true"}, args...), Dir: dir, Env: f.env, Stdout: &out, Stderr: &errb,
		})
		if err != nil {
			t.Fatal(err)
		}
		code, err := p.Wait()
		if err != nil {
			t.Fatal(err)
		}
		return out.String(), strings.TrimSpace(errb.String()), code
	}
	listTree := func() []string {
		var names []string
		_ = filepath.WalkDir(wt, func(p string, de os.DirEntry, err error) error {
			if err == nil {
				rel, _ := filepath.Rel(wt, p)
				names = append(names, rel)
			}
			return nil
		})
		sort.Strings(names)
		return names
	}

	_, _, code := gitCode(wt, "status", "--porcelain")
	t.Logf("before: status exit %d, tree %v", code, listTree())

	_, se, code := gitCode(d, "worktree", "remove", wt)
	t.Logf("attempt 1 (handle held): exit %d, message %q", code, se)
	_, statErr := os.Stat(wt)
	t.Logf("attempt 1: folder exists=%v, tree now %v", statErr == nil, listTree())
	list, _, _ := gitCode(d, "worktree", "list", "--porcelain")
	t.Logf("attempt 1: git still lists the worktree=%v", strings.Contains(strings.ToLower(filepath.ToSlash(list)), strings.ToLower(filepath.ToSlash(wt))))
	if statErr == nil {
		so, se2, code2 := gitCode(wt, "status", "--porcelain")
		t.Logf("attempt 1: status in the worktree: exit %d, out %q, err %q", code2, so, se2)
	}

	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true

	_, se, code = gitCode(d, "worktree", "remove", wt)
	t.Logf("attempt 2 (handle closed, no extra flag): exit %d, message %q", code, se)
	_, statErr = os.Stat(wt)
	t.Logf("attempt 2: folder exists=%v, tree now %v", statErr == nil, listTree())
	list, _, _ = gitCode(d, "worktree", "list", "--porcelain")
	t.Logf("attempt 2: git still lists the worktree=%v", strings.Contains(strings.ToLower(filepath.ToSlash(list)), strings.ToLower(filepath.ToSlash(wt))))
}
