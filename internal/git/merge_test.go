package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merge runs no hook of the repo: not the ones in .git/hooks, not the ones a
// core.hooksPath in the repo config points at (an agent may have written them).
func TestMergeRunsNoHooks(t *testing.T) {
	f := newFx(t)
	d := f.repo("hooks")
	f.commit(d, "a.txt", "one\n", "init")
	f.git(d, "switch", "-qc", "feature")
	f.commit(d, "b.txt", "two\n", "feature b")
	tip := strings.TrimSpace(f.git(d, "rev-parse", "HEAD"))
	f.git(d, "switch", "-q", "main")
	f.commit(d, "c.txt", "three\n", "main c")

	marker := filepath.Join(t.TempDir(), "hook-ran")
	script := "#!/bin/sh\necho ran >> \"" + filepath.ToSlash(marker) + "\"\nexit 1\n"
	hooksPathDir := filepath.Join(t.TempDir(), "my-hooks")
	for _, dir := range []string{filepath.Join(d, ".git", "hooks"), hooksPathDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"pre-merge-commit", "commit-msg", "prepare-commit-msg", "post-merge", "post-commit"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil { //nolint:gosec // test hook
				t.Fatal(err)
			}
		}
	}
	f.git(d, "config", "core.hooksPath", hooksPathDir)

	id, err := f.g.MergeNoFF(bg(), d, tip, "Merge feature")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repo hook ran during the merge")
	}
	if head, _ := f.g.Head(bg(), d); head != id {
		t.Errorf("head = %s, merge returned %s", head, id)
	}
}

// Untracked files that the merge would overwrite make git refuse: a plain
// error, nothing merged, no merge left in progress.
func TestMergeRefusedByUntrackedFileIsAPlainError(t *testing.T) {
	f := newFx(t)
	d := f.repo("untracked")
	f.commit(d, "a.txt", "one\n", "init")
	f.git(d, "switch", "-qc", "feature")
	f.commit(d, "b.txt", "two\n", "feature b")
	tip := strings.TrimSpace(f.git(d, "rev-parse", "HEAD"))
	f.git(d, "switch", "-q", "main")
	f.commit(d, "c.txt", "three\n", "main c")
	f.write(d, "b.txt", "mine\n")

	_, err := f.g.MergeNoFF(bg(), d, tip, "Merge feature")
	var ce *MergeConflictError
	var ge *Error
	if err == nil || errors.As(err, &ce) || !errors.As(err, &ge) || ge.UserMessage() == "" {
		t.Fatalf("merge = %v, want a plain *Error", err)
	}
	if in, _, _ := f.g.MergeInProgress(bg(), d); in {
		t.Error("a refused merge is in progress")
	}
	if got, _ := os.ReadFile(filepath.Join(d, "b.txt")); string(got) != "mine\n" {
		t.Errorf("the user's file was changed: %q", got)
	}
}

// The merge commit message is the one given, and the merge never pushes (the
// repo here has no remote at all).
func TestMergeMessageAndParents(t *testing.T) {
	f := newFx(t)
	d := f.repo("msg")
	f.commit(d, "a.txt", "one\n", "init")
	f.git(d, "switch", "-qc", "feature")
	f.commit(d, "b.txt", "two\n", "feature b")
	tip := strings.TrimSpace(f.git(d, "rev-parse", "HEAD"))
	f.git(d, "switch", "-q", "main")
	f.commit(d, "c.txt", "three\n", "main c")

	msg := "Merge branch feature (ygg/abc)\n\nAdds b."
	id, err := f.g.MergeNoFF(bg(), d, tip, msg)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.g.HeadInfo(bg(), d)
	if err != nil || info.ID != id || info.Message != msg {
		t.Fatalf("HEAD = %+v, %v; want %s with message %q", info, err, id, msg)
	}
}

// A fast-forwardable branch still gets a merge commit.
func TestMergeNeverFastForwards(t *testing.T) {
	f := newFx(t)
	d := f.repo("noff")
	f.commit(d, "a.txt", "one\n", "init")
	before, _ := f.g.Head(bg(), d)
	f.git(d, "switch", "-qc", "feature")
	f.commit(d, "b.txt", "two\n", "feature b")
	tip := strings.TrimSpace(f.git(d, "rev-parse", "HEAD"))
	f.git(d, "switch", "-q", "main")

	id, err := f.g.MergeNoFF(bg(), d, tip, "Merge feature")
	if err != nil {
		t.Fatal(err)
	}
	if id == tip {
		t.Fatal("fast-forwarded: no merge commit")
	}
	parents := strings.Fields(f.git(d, "rev-list", "--parents", "-n", "1", id))
	if len(parents) != 3 || parents[1] != before || parents[2] != tip {
		t.Fatalf("parents = %v, want [%s %s]", parents[1:], before, tip)
	}
}

// Ignored files are not part of what the agent changed; untracked ones are.
func TestDiffAgainstBaseSkipsIgnoredFiles(t *testing.T) {
	f := newFx(t)
	d := f.repo("ign")
	f.commit(d, ".gitignore", "*.log\nbuild/\n", "init")
	base, _ := f.g.Head(bg(), d)
	f.write(d, "debug.log", "noise\n")
	f.write(d, "build/out.bin", "x")
	f.write(d, "src/keep.txt", "k\n")
	diff, err := f.g.DiffAgainstBase(bg(), d, base, BaseDiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "src/keep.txt" {
		t.Fatalf("files = %+v, want only src/keep.txt", diff.Files)
	}
	f.git(d, "add", "-A")
	f.git(d, "commit", "-q", "-m", "keep")
	if has, err := f.g.HasChanges(bg(), d, base); err != nil || !has {
		t.Fatalf("HasChanges after a commit = %v, %v", has, err)
	}
	head, _ := f.g.Head(bg(), d)
	if has, err := f.g.HasChanges(bg(), d, head); err != nil || has {
		t.Fatalf("HasChanges against HEAD with only ignored files = %v, %v", has, err)
	}
}

// DiffAgainstBase leaves the repo as it found it: index, files and config.
func TestDiffAgainstBaseChangesNothing(t *testing.T) {
	f := newFx(t)
	d := f.repo("ro")
	f.commit(d, "a.txt", "one\n", "init")
	base, _ := f.g.Head(bg(), d)
	f.write(d, "a.txt", "one\r\n")
	f.write(d, "new.txt", "n\n")
	status := f.git(d, "status", "--porcelain=v2", "--untracked-files=all")
	config, _ := os.ReadFile(filepath.Join(d, ".git", "config"))
	for range 2 {
		if _, err := f.g.DiffAgainstBase(bg(), d, base, BaseDiffOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.git(d, "status", "--porcelain=v2", "--untracked-files=all"); got != status {
		t.Errorf("status changed:\n%s\nvs\n%s", status, got)
	}
	if got, _ := os.ReadFile(filepath.Join(d, ".git", "config")); string(got) != string(config) {
		t.Error("repo config changed")
	}
	if got, _ := os.ReadFile(filepath.Join(d, "a.txt")); string(got) != "one\r\n" {
		t.Errorf("a.txt rewritten: %q", got)
	}
}

// A path that looks like an option is a file name, not a flag.
func TestDiffAgainstBaseOddFileNames(t *testing.T) {
	f := newFx(t)
	d := f.repo("odd")
	f.commit(d, "a.txt", "one\n", "init")
	base, _ := f.g.Head(bg(), d)
	f.write(d, "-rf.txt", "x\n")
	f.write(d, "[a] x.txt", "y\n")
	f.commit(d, "[b].txt", "z\n", "tracked glob name")
	f.write(d, "[b].txt", "z2\n")
	diff, err := f.g.DiffAgainstBase(bg(), d, base, BaseDiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, fl := range diff.Files {
		got[fl.Path] = fl.Status
	}
	want := map[string]string{"-rf.txt": FileAdded, "[a] x.txt": FileAdded, "[b].txt": FileAdded}
	for p, st := range want {
		if got[p] != st {
			t.Errorf("%s: status %q, want %q (all: %v)", p, got[p], st, got)
		}
	}
	for _, fl := range diff.Files {
		if !strings.Contains(fl.Patch, "diff --git") {
			t.Errorf("%s has no patch: %q", fl.Path, fl.Patch)
		}
	}
}
