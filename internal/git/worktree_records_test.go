package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Paths are compared the way the disk does: on a case-insensitive file system
// a different spelling of the same folder is the same path, a different folder
// never is.
func TestSamePathFollowsTheDisk(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "wt-a")
	if !samePath(a, a+string(filepath.Separator)) || !samePath(a, filepath.Join(root, ".", "wt-a")) {
		t.Error("the same path spelled with a trailing separator or a dot differs")
	}
	if samePath(a, filepath.Join(root, "wt-b")) {
		t.Error("two different folders compare equal")
	}
	if platform.PathsCaseInsensitive {
		if !samePath(a, filepath.Join(strings.ToUpper(root), "WT-A")) {
			t.Error("case-only difference counts as another path on a case-insensitive file system")
		}
	} else if samePath(a, filepath.Join(root, "WT-A")) {
		t.Error("case-only difference is the same path on a case-sensitive file system")
	}
}

// The record of a worktree whose folder is gone is marked prunable, and
// WorktreePrune drops exactly that record. A worktree whose folder exists, and
// the main checkout, stay.
func TestWorktreePrunesOnlyVanishedFolders(t *testing.T) {
	f := newFx(t)
	d := f.repo("wtrecords")
	f.commit(d, "a.txt", "one\n", "init")
	root := t.TempDir()
	gone := filepath.Join(root, "wt-gone")
	kept := filepath.Join(root, "wt-kept")
	if _, err := f.g.WorktreeAdd(bg(), d, gone, "ygg/gone", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.g.WorktreeAdd(bg(), d, kept, "ygg/kept", ""); err != nil {
		t.Fatal(err)
	}
	// Move the folder away, as the landing flow does: git still lists it.
	if err := os.Rename(gone, filepath.Join(root, "staged")); err != nil {
		t.Fatal(err)
	}
	list, err := f.g.WorktreeList(bg(), d)
	if err != nil {
		t.Fatal(err)
	}
	var prunable []string
	for _, w := range list {
		if w.Prunable {
			prunable = append(prunable, w.Path)
		}
	}
	if len(prunable) != 1 || !samePath(prunable[0], gone) {
		t.Fatalf("prunable entries = %v, want only %s", prunable, gone)
	}
	if OtherPrunable(list, gone) {
		t.Error("the only prunable entry is the one removed: OtherPrunable must be false")
	}
	if !OtherPrunable(list, kept) {
		t.Error("an entry other than kept is prunable: OtherPrunable must be true")
	}

	if err := f.g.WorktreePrune(bg(), d); err != nil {
		t.Fatal(err)
	}
	after, err := f.g.WorktreeList(bg(), d)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, w := range after {
		paths = append(paths, w.Path)
		if w.Prunable {
			t.Errorf("%s still prunable", w.Path)
		}
	}
	if len(after) != 2 || !samePath(paths[1], kept) {
		t.Fatalf("worktrees after = %v, want the main checkout and %s", paths, kept)
	}
	// The kept worktree still works.
	if _, err := f.g.Status(bg(), kept); err != nil {
		t.Errorf("kept worktree broke: %v", err)
	}
	// Nothing to do is not an error.
	if err := f.g.WorktreePrune(bg(), d); err != nil {
		t.Errorf("second pass: %v", err)
	}
}
