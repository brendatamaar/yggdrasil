package git

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety/safetytest"
)

func TestSnapshotContract(t *testing.T) {
	f := newFx(t)
	safetytest.SnapshotterContract(t, safetytest.SnapshotHarness{
		Snap: f.g,
		NewRepo: func(t *testing.T) string {
			d := f.repo("r")
			f.write(d, "a.txt", "one\n")
			f.write(d, "tool.sh", "echo\n")
			f.git(d, "add", "-A")
			f.git(d, "commit", "-q", "-m", "init")
			return d
		},
		NonRepo: func(t *testing.T) string { return t.TempDir() },
		Commit: func(t *testing.T, d string) {
			f.git(d, "add", "-A")
			f.git(d, "commit", "-q", "--allow-empty", "-m", "next")
		},
		Ignore: func(t *testing.T, d, name string) {
			f.write(d, ".gitignore", name+"\n")
		},
		SetExec: func(t *testing.T, d, rel string) {
			f.git(d, "update-index", "--chmod=+x", rel)
		},
		Oversize: func(t *testing.T, d string) {
			// A file one byte over the byte cap. Truncate only sets the size;
			// the snapshot must refuse by size before it reads anything.
			fh, err := os.Create(filepath.Join(d, "huge.bin"))
			if err != nil {
				t.Fatal(err)
			}
			defer fh.Close()
			if err := fh.Truncate(snapshotMaxBytes + 1); err != nil {
				t.Skip("cannot make a large file here: " + err.Error())
			}
		},
		IsTooLarge: func(err error) bool { return errors.Is(err, ErrSnapshotTooLarge) },
		NewRef: func(t *testing.T, d string) {
			f.git(d, "branch", "extra-branch")
		},
	})
}

func TestSnapshotTooLargeHasPlainMessage(t *testing.T) {
	f := newFx(t)
	d := f.repo("big")
	f.write(d, "a.txt", "one\n")
	f.git(d, "add", "-A")
	f.git(d, "commit", "-q", "-m", "init")
	fh, err := os.Create(filepath.Join(d, "huge.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(snapshotMaxBytes + 1); err != nil {
		t.Skip("cannot make a large file here: " + err.Error())
	}
	_ = fh.Close()
	_, err = f.g.Snapshot(t.Context(), d)
	var ge *Error
	if !errors.Is(err, ErrSnapshotTooLarge) || !errors.As(err, &ge) || ge.UserMsg == "" || strings.Contains(ge.UserMsg, d) {
		t.Fatalf("err = %v", err)
	}
}

func TestUntrackedBudgetCountsFilesAndBytes(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := sha256.New()
	files := &untrackedBudget{ctx: t.Context(), maxFiles: 2, maxBytes: 1 << 20}
	var err error
	for _, n := range []string{"a", "b", "c"} {
		err = hashUntracked(h, dir, n, files)
	}
	if !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("file cap: err = %v", err)
	}
	bytesCap := &untrackedBudget{ctx: t.Context(), maxFiles: 10, maxBytes: 7}
	if err := hashUntracked(h, dir, "a", bytesCap); err != nil {
		t.Fatal(err)
	}
	if err := hashUntracked(h, dir, "b", bytesCap); !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("byte cap: err = %v", err)
	}
}

func TestUntrackedHashStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	b := &untrackedBudget{ctx: ctx, maxFiles: 10, maxBytes: 1 << 30}
	if err := hashUntracked(sha256.New(), dir, "a", b); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSnapshotNoCommitsIsError(t *testing.T) {
	f := newFx(t)
	d := f.repo("empty")
	if _, err := f.g.Snapshot(t.Context(), d); err == nil {
		t.Fatal("snapshot of a repo without commits succeeded")
	}
}
