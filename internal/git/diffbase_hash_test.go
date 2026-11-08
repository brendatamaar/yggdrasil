package git

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func headOf(f *fx, dir string) string {
	return strings.TrimSpace(f.git(dir, "rev-parse", "HEAD"))
}

func fileOf(t *testing.T, d BaseDiff, path string) BaseFile {
	t.Helper()
	for _, bf := range d.Files {
		if bf.Path == path {
			return bf
		}
	}
	t.Fatalf("%s not in diff %+v", path, d.Files)
	return BaseFile{}
}

// A text file's hash is the SHA-256 of its patch, and it follows the change.
func TestBaseDiffHashIsPatchSHA256AndFollowsEdits(t *testing.T) {
	f := newFx(t)
	dir := f.repo("hash")
	f.commit(dir, "a.txt", "one\n", "init")
	base := headOf(f, dir)

	f.write(dir, "a.txt", "one\ntwo\n")
	d1, err := f.g.DiffAgainstBase(bg(), dir, base, BaseDiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f1 := fileOf(t, d1, "a.txt")
	sum := sha256.Sum256([]byte(f1.Patch))
	if d1.FilesTruncated || f1.PatchTruncated || f1.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash %q is not sha256 of the patch (truncated %v/%v)", f1.Hash, d1.FilesTruncated, f1.PatchTruncated)
	}

	f.write(dir, "a.txt", "one\ntwo\nthree\n")
	d2, _ := f.g.DiffAgainstBase(bg(), dir, base, BaseDiffOptions{})
	if fileOf(t, d2, "a.txt").Hash == f1.Hash {
		t.Error("hash did not change after another edit: notes would never go outdated")
	}
}

// A large patch cuts Patch but is not a files-truncated read, and the hash
// still follows edits, even one past the cut.
func TestBaseDiffLargePatchKeepsAFollowingHash(t *testing.T) {
	f := newFx(t)
	dir := f.repo("big")
	f.commit(dir, "big.txt", "x\n", "init")
	base := headOf(f, dir)
	body := strings.Repeat("a long line of changed text\n", 20000)
	f.write(dir, "big.txt", body)
	d1, err := f.g.DiffAgainstBase(bg(), dir, base, BaseDiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b1 := fileOf(t, d1, "big.txt")
	if !b1.PatchTruncated || !d1.Truncated || d1.FilesTruncated {
		t.Fatalf("want a cut patch but a whole file list: %v %v %v", b1.PatchTruncated, d1.Truncated, d1.FilesTruncated)
	}
	f.write(dir, "big.txt", body+"tail\n")
	d2, _ := f.g.DiffAgainstBase(bg(), dir, base, BaseDiffOptions{})
	if fileOf(t, d2, "big.txt").Hash == b1.Hash {
		t.Error("an edit past the cut did not change the hash")
	}
}

func TestBaseDiffFilesTruncatedOnlyByFileCount(t *testing.T) {
	f := newFx(t)
	dir := f.repo("many")
	f.commit(dir, "a.txt", "1\n", "init")
	base := headOf(f, dir)
	f.write(dir, "b.txt", "b\n")
	f.write(dir, "c.txt", "c\n")
	d, err := f.g.DiffAgainstBase(bg(), dir, base, BaseDiffOptions{MaxFiles: 1})
	if err != nil || !d.FilesTruncated {
		t.Fatalf("FilesTruncated = %v, %v", d.FilesTruncated, err)
	}
}

func TestBaseDiffEOLOnlyChangeChangesHash(t *testing.T) {
	f := newFx(t)
	dir := f.repo("eol")
	f.git(dir, "config", "core.autocrlf", "false")
	f.commit(dir, "a.txt", "one\ntwo\n", "init")
	base := headOf(f, dir)
	f.write(dir, "a.txt", "one\r\ntwo\r\n")
	d1, err := f.g.DiffAgainstBase(bg(), dir, base, BaseDiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := fileOf(t, d1, "a.txt")
	if !a.EOLOnly || a.Hash == "" {
		t.Fatalf("want an EOL-only change with a hash: %+v", a)
	}
	f.write(dir, "a.txt", "one\ntwo\nx\n")
	d2, _ := f.g.DiffAgainstBase(bg(), dir, base, BaseDiffOptions{})
	if fileOf(t, d2, "a.txt").Hash == a.Hash {
		t.Error("EOL-only and content change share a hash")
	}
}
