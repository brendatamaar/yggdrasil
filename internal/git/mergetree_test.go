package git

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const oid40 = "8197f624652b8976b296c8d5d76b405f7a824f70"

func TestParseMergeTree(t *testing.T) {
	ok := []struct {
		name string
		out  string
		exit int
		want Prediction
	}{
		{"merged", oid40 + "\x00", 0, Prediction{Clean: true, Tree: oid40}},
		{"merged with messages", oid40 + "\x00\x001\x00f.txt\x00Auto-merging\x00Auto-merging f.txt\n\x00", 0, Prediction{Clean: true, Tree: oid40}},
		{"conflicts sorted", oid40 + "\x00sp ace.txt\x00f.txt\x00\x001\x00f.txt\x00CONFLICT (contents)\x00msg\n\x00", 1, Prediction{Files: []string{"f.txt", "sp ace.txt"}, Tree: oid40}},
		{"non-ascii names", oid40 + "\x00café.txt\x00\x00", 1, Prediction{Files: []string{"café.txt"}, Tree: oid40}},
		{"no names with exit 1", oid40 + "\x00", 1, Prediction{Tree: oid40}},
		{"sha256 tree id", strings.Repeat("ab", 32) + "\x00x\x00", 1, Prediction{Files: []string{"x"}, Tree: strings.Repeat("ab", 32)}},
	}
	for _, c := range ok {
		got, err := ParseMergeTree(c.out, c.exit)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Clean != c.want.Clean || got.Tree != c.want.Tree || !reflect.DeepEqual(append([]string(nil), got.Files...), append([]string(nil), c.want.Files...)) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}

	bad := []struct {
		name, out string
		exit      int
	}{
		{"empty output", "", 0},
		{"no NUL after the id", oid40, 0},
		{"short id", "abc123\x00", 0},
		{"upper-case id", strings.ToUpper(oid40) + "\x00", 0},
		{"id with a name glued on", oid40 + "f.txt\x00", 1},
		{"not an id", "CONFLICT (content): Merge conflict in f.txt\x00", 1},
		{"newline separated (no -z)", oid40 + "\nf.txt\n", 1},
		{"tab in a name", oid40 + "\x00a\tb.txt\x00\x00", 1},
		{"control character in a name", oid40 + "\x00a\nb.txt\x00\x00", 1},
		{"escape in a name", oid40 + "\x00a\x1bb.txt\x00\x00", 1},
		{"DEL in a name", oid40 + "\x00a\x7fb.txt\x00\x00", 1},
		{"files with a zero exit", oid40 + "\x00f.txt\x00\x00", 0},
		{"exit 2", oid40 + "\x00", 2},
		{"exit 128", "", 128},
		{"exit -1", oid40 + "\x00", -1},
	}
	for _, c := range bad {
		if _, err := ParseMergeTree(c.out, c.exit); err == nil {
			t.Errorf("%s: accepted %q (exit %d)", c.name, c.out, c.exit)
		}
	}
}

// Whatever ParseMergeTree accepts is a valid prediction: a tree id, sorted
// names without control characters, no names when clean.
func FuzzParseMergeTree(f *testing.F) {
	f.Add(oid40+"\x00", 0)
	f.Add(oid40+"\x00b\x00a\x00\x00m\x00", 1)
	f.Add("", 1)
	f.Add(oid40+"\x00a\nb\x00", 1)
	f.Fuzz(func(t *testing.T, out string, exit int) {
		p, err := ParseMergeTree(out, exit)
		if err != nil {
			return
		}
		if !isCommitID(p.Tree) {
			t.Fatalf("accepted a bad tree id %q", p.Tree)
		}
		if p.Clean != (exit == 0) || (p.Clean && len(p.Files) > 0) {
			t.Fatalf("merged flag %v with exit %d and %d files", p.Clean, exit, len(p.Files))
		}
		for i, n := range p.Files {
			if n == "" || strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				t.Fatalf("accepted file name %q", n)
			}
			if i > 0 && p.Files[i-1] > n {
				t.Fatalf("files not sorted: %q", p.Files)
			}
		}
	})
}

func TestMergeTreeRealGit(t *testing.T) {
	f := newFx(t)
	d := f.repo("mt")
	f.commit(d, "f.txt", "a\nb\nc\n", "init")
	f.commit(d, "sp ace.txt", "x\n", "second")
	base := f.git(d, "rev-parse", "HEAD")
	_ = base
	f.git(d, "switch", "-qc", "feature")
	f.commit(d, "f.txt", "a\nB\nc\n", "feature f")
	f.commit(d, "sp ace.txt", "y\n", "feature sp")
	f.git(d, "switch", "-q", "main")
	f.commit(d, "f.txt", "a\nQ\nc\n", "main f")
	f.commit(d, "sp ace.txt", "z\n", "main sp")

	p, err := f.g.MergeTreeWriteTree(bg(), d, "main", "feature")
	if errors.Is(err, ErrMergeTreeUnsupported) {
		t.Skip("git is older than 2.38")
	}
	if err != nil {
		t.Fatal(err)
	}
	if p.Clean || !reflect.DeepEqual(p.Files, []string{"f.txt", "sp ace.txt"}) {
		t.Errorf("prediction = %+v, want conflicts in f.txt and sp ace.txt", p)
	}
	// Nothing in the working tree or index changed.
	if st, _ := f.g.Status(bg(), d); st.Dirty() || len(st.Files) != 0 {
		t.Errorf("merge-tree touched the tree: %+v", st)
	}
	if in, _, _ := f.g.MergeInProgress(bg(), d); in {
		t.Error("merge-tree left a merge in progress")
	}

	f.git(d, "switch", "-qc", "other", "HEAD~2")
	f.commit(d, "n.txt", "n\n", "other n")
	p, err = f.g.MergeTreeWriteTree(bg(), d, "other", "feature")
	if err != nil || !p.Clean || len(p.Files) != 0 {
		t.Errorf("merged prediction = %+v, %v", p, err)
	}
	if _, err := f.g.MergeTreeWriteTree(bg(), d, "-x", "feature"); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("option-like revision = %v, want ErrInvalidArg", err)
	}
	if _, err := f.g.MergeTreeWriteTree(bg(), d, "nope", "feature"); err == nil {
		t.Error("unknown revision accepted")
	}
}
