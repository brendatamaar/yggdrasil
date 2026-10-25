package git

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Tests that isolate one guard each, so removing that guard (and only it)
// fails exactly one test. See the mutation list in the commit message of this
// file for the evidence.

// The literal-insert guard in applyDelta (`len(out)+n > rs`). The final
// `len(out) != rs` check would reject the same deltas, so the result alone
// cannot tell the guard is there: the memory used before rejecting can.
func TestApplyDelta_LiteralGuardStopsBeforeGrowing(t *testing.T) {
	base := []byte("b")
	delta := append(varintLE(1), varintLE(1)...) // base 1 byte, result 1 byte
	delta = append(delta, dInsert([]byte("x"))...)
	chunk := make([]byte, 127)
	for range 20000 { // 2.5 MB of literals after the result is already full
		delta = append(delta, dInsert(chunk)...)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := applyDelta(base, delta)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("literals beyond the declared result size were accepted")
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 512<<10 {
		t.Errorf("applyDelta allocated %d bytes before rejecting; the literal guard should stop it at the first extra insert", grown)
	}
}

// replace refs can hide in packed-refs only: the read must decline.
func TestReadCommitDirect_PackedRefsReplaceDeclines(t *testing.T) {
	dir, obj := fakeRepo(t)
	oid := putCommit(t, obj, commitRaw(goodAuthor, "", "s\n"))
	if _, ok := readCommitDirect(dir, oid); !ok {
		t.Fatal("control: plain fake repo not answered")
	}
	pr := filepath.Join(dir, ".git", "packed-refs")
	write := func(s string) {
		if err := os.WriteFile(pr, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("# pack-refs with: peeled fully-peeled sorted\n" + oid + " refs/heads/main\n")
	if _, ok := readCommitDirect(dir, oid); !ok {
		t.Error("packed-refs without replace refs should not decline")
	}
	write(oid + " refs/replace/" + oid + "\n")
	if _, ok := readCommitDirect(dir, oid); ok {
		t.Error("answered although packed-refs holds a replace ref")
	}
	write(strings.Repeat("# padding line to make the file large\n", 40000))
	if _, ok := readCommitDirect(dir, oid); ok {
		t.Error("answered although packed-refs is too big to check")
	}
}

func TestReadCommitDirect_RefsReplaceDirDeclines(t *testing.T) {
	dir, obj := fakeRepo(t)
	oid := putCommit(t, obj, commitRaw(goodAuthor, "", "s\n"))
	if err := os.MkdirAll(filepath.Join(dir, ".git", "refs", "replace"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCommitDirect(dir, oid); ok {
		t.Error("answered although refs/replace exists")
	}
}

func TestReadCommitDirect_AlternatesFileDeclines(t *testing.T) {
	dir, obj := fakeRepo(t)
	oid := putCommit(t, obj, commitRaw(goodAuthor, "", "s\n"))
	if err := os.MkdirAll(filepath.Join(obj, "info"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(obj, "info", "alternates"), []byte("../elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCommitDirect(dir, oid); ok {
		t.Error("answered although the repo has alternates (objects may live outside the tracked repo)")
	}
}

// The SHA-1 check on a loose object: a well-formed commit stored under another
// commit's id is not returned.
func TestReadCommitDirect_LooseContentMustHashToID(t *testing.T) {
	dir, obj := fakeRepo(t)
	real := commitRaw(goodAuthor, "", "real\n")
	forged := commitRaw("Mallory <m@example.test> 1 +0000", "", "forged\n")
	oid := objID("commit", real)
	putLoose(t, obj, oid, append([]byte("commit "+itoa(len(forged))+"\x00"), forged...))
	if c, ok := readCommitDirect(dir, oid); ok {
		t.Errorf("returned %+v for content that does not hash to the id", c)
	}
}

// A directory in .git that is a junction (Windows) or symlink must not be
// followed. Symlinks need a privilege on Windows; junctions do not, see
// fastcommit_junction_windows_test.go.
func TestReadCommitDirect_SymlinkedObjectsDirDeclines(t *testing.T) {
	dir, obj := fakeRepo(t)
	oid := putCommit(t, obj, commitRaw(goodAuthor, "", "s\n"))
	moved := obj + "-real"
	if err := os.Rename(obj, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, obj); err != nil {
		t.Skipf("symlinks cannot be created here (needs a privilege on Windows): %v", err)
	}
	if _, ok := readCommitDirect(dir, oid); ok {
		t.Error("followed a symlinked objects folder")
	}
}
