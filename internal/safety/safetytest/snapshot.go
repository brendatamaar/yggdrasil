package safetytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// FakeSnapshotter is a safety.Snapshotter over plain folders. A "repo" is a
// folder with a .fakehead file (the commit). .fakeignore lists ignored
// repo-relative paths, one per line (a folder name ignores everything below).
// Every other file takes part in the hash, so a fake tree behaves like a git
// tree for the contract.
type FakeSnapshotter struct{}

// Snapshot implements safety.Snapshotter.
func (FakeSnapshotter) Snapshot(ctx context.Context, dir string) (guard.Snapshot, error) {
	head, err := os.ReadFile(filepath.Join(dir, ".fakehead")) //nolint:gosec // test fake on a temp dir
	if err != nil {
		return guard.Snapshot{}, errors.New("this folder is not a repository")
	}
	var ignored []string
	if b, err := os.ReadFile(filepath.Join(dir, ".fakeignore")); err == nil { //nolint:gosec // test fake on a temp dir
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				ignored = append(ignored, l)
			}
		}
	}
	h := sha256.New()
	var paths []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." || d.IsDir() || rel == ".fakehead" {
			return nil
		}
		for _, ig := range ignored {
			if rel == ig || strings.HasPrefix(rel, strings.TrimSuffix(ig, "/")+"/") {
				return nil
			}
		}
		b, err := os.ReadFile(p) //nolint:gosec // test fake on a temp dir
		if err != nil {
			return err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return guard.Snapshot{}, err
	}
	sort.Strings(paths)
	sum := hex.EncodeToString(h.Sum(nil))
	return guard.Snapshot{Head: strings.TrimSpace(string(head)), Status: sum, DiffHash: sum, Paths: paths}, nil
}

// SnapshotHarness gives the contract the operations that differ between the
// fake and a real git repo. Plain file edits are done by the contract itself.
type SnapshotHarness struct {
	Snap safety.Snapshotter
	// NewRepo returns a repo whose HEAD holds a.txt ("one\n") and tool.sh.
	NewRepo func(t *testing.T) string
	// NonRepo returns a folder that is not a repo.
	NonRepo func(t *testing.T) string
	// Commit stages and commits everything.
	Commit func(t *testing.T, dir string)
	// Ignore makes the repo ignore the repo-relative file name.
	Ignore func(t *testing.T, dir, name string)
	// SetExec changes the executable mode of a tracked file.
	SetExec func(t *testing.T, dir, rel string)
}

func writeFile(t *testing.T, p, c string, appendTo bool) {
	t.Helper()
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendTo {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(p, flag, 0o600) //nolint:gosec // test helper on a temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(c); err != nil {
		t.Fatal(err)
	}
}

// FakeHarness is the harness for FakeSnapshotter.
func FakeHarness() SnapshotHarness {
	return SnapshotHarness{
		Snap: FakeSnapshotter{},
		NewRepo: func(t *testing.T) string {
			d := t.TempDir()
			writeFile(t, filepath.Join(d, ".fakehead"), "c1", false)
			writeFile(t, filepath.Join(d, "a.txt"), "one\n", false)
			writeFile(t, filepath.Join(d, "tool.sh"), "echo\n", false)
			return d
		},
		NonRepo: func(t *testing.T) string { return t.TempDir() },
		Commit: func(t *testing.T, d string) {
			b, _ := os.ReadFile(filepath.Join(d, ".fakehead")) //nolint:gosec // temp dir
			writeFile(t, filepath.Join(d, ".fakehead"), string(b)+"x", false)
		},
		Ignore: func(t *testing.T, d, name string) {
			writeFile(t, filepath.Join(d, ".fakeignore"), name+"\n", true)
		},
		SetExec: func(t *testing.T, d, rel string) {
			writeFile(t, filepath.Join(d, ".fakemodes"), rel+"\n", true)
		},
	}
}

// SnapshotterContract is run against the fake and the real snapshotter.
func SnapshotterContract(t *testing.T, h SnapshotHarness) {
	write := func(t *testing.T, dir, rel, c string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, c, false)
	}
	remove := func(t *testing.T, dir, rel string) {
		t.Helper()
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	snap := func(t *testing.T, dir string) guard.Snapshot {
		t.Helper()
		s, err := h.Snap.Snapshot(context.Background(), dir)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if s.Head == "" || s.Status == "" || s.DiffHash == "" {
			t.Fatalf("snapshot has an empty field: %+v", s)
		}
		return s
	}
	// check prepares a fresh repo, takes a snapshot, mutates, and compares.
	check := func(t *testing.T, name string, prepare, mutate func(t *testing.T, dir string), differs bool) {
		t.Run(name, func(t *testing.T) {
			t.Parallel() // each case has its own repo; git start-up dominates the time
			dir := h.NewRepo(t)
			if prepare != nil {
				prepare(t, dir)
			}
			before := snap(t, dir)
			if !before.Equal(snap(t, dir)) {
				t.Fatal("two snapshots of an untouched tree differ")
			}
			mutate(t, dir)
			after := snap(t, dir)
			if differs && before.Equal(after) {
				t.Fatal("the change went unnoticed")
			}
			if !differs && !before.Equal(after) {
				t.Fatal("the change should not be seen")
			}
		})
	}

	check(t, "edit tracked file", nil, func(t *testing.T, d string) { write(t, d, "a.txt", "two\n") }, true)
	check(t, "new untracked file", nil, func(t *testing.T, d string) { write(t, d, "new.txt", "x") }, true)
	check(t, "edit of an already untracked file",
		func(t *testing.T, d string) { write(t, d, "scratch/n.txt", "aaaa") },
		func(t *testing.T, d string) { write(t, d, "scratch/n.txt", "aaab") }, true)
	check(t, "delete tracked file", nil, func(t *testing.T, d string) { remove(t, d, "a.txt") }, true)
	check(t, "delete untracked file",
		func(t *testing.T, d string) { write(t, d, "n.txt", "x") },
		func(t *testing.T, d string) { remove(t, d, "n.txt") }, true)
	check(t, "mode change", nil, func(t *testing.T, d string) { h.SetExec(t, d, "tool.sh") }, true)
	check(t, "new commit", nil, func(t *testing.T, d string) {
		write(t, d, "c.txt", "c")
		h.Commit(t, d)
	}, true)
	check(t, "rewrite with the same content", nil, func(t *testing.T, d string) { write(t, d, "a.txt", "one\n") }, false)
	check(t, "CRLF flip", nil, func(t *testing.T, d string) { write(t, d, "a.txt", "one\r\n") }, true)
	check(t, "ignored file",
		func(t *testing.T, d string) {
			h.Ignore(t, d, "build.log")
			h.Commit(t, d)
		},
		func(t *testing.T, d string) { write(t, d, "build.log", "noise") }, false)

	t.Run("not a repository", func(t *testing.T) {
		if _, err := h.Snap.Snapshot(context.Background(), h.NonRepo(t)); err == nil {
			t.Fatal("a plain folder gave a snapshot")
		}
	})
	t.Run("changed paths are listed", func(t *testing.T) {
		dir := h.NewRepo(t)
		write(t, dir, "a.txt", "two\n")
		write(t, dir, "n/new.txt", "x")
		got := strings.Join(snap(t, dir).Paths, ",")
		if !strings.Contains(got, "a.txt") || !strings.Contains(got, "n/new.txt") {
			t.Fatalf("Paths = %v", got)
		}
	})
}
