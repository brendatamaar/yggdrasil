package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sameAsLog checks that ReadCommitDirect answers exactly what git log shows
// (Short aside), or reports that it cannot answer (wantOK=false).
func sameAsLog(t *testing.T, f *fx, dir string, wantOK bool) {
	t.Helper()
	cs, err := f.g.Log(context.Background(), dir, 1)
	if err != nil || len(cs) != 1 {
		t.Fatalf("log: %v %v", cs, err)
	}
	want := cs[0]
	got, ok := ReadCommitDirect(dir, want.Hash)
	if ok != wantOK {
		t.Fatalf("ReadCommitDirect ok = %v, want %v", ok, wantOK)
	}
	if !ok {
		return
	}
	if got.Hash != want.Hash || got.Author != want.Author || got.Email != want.Email ||
		got.Subject != want.Subject || !got.Date.Equal(want.Date) ||
		got.Date.Format("2006-01-02T15:04:05Z07:00") != want.Date.Format("2006-01-02T15:04:05Z07:00") ||
		strings.Join(got.Parents, " ") != strings.Join(want.Parents, " ") {
		t.Errorf("direct = %+v\n  git  = %+v", got, want)
	}
}

func TestReadCommitDirect_LooseMatchesLog(t *testing.T) {
	f := newFx(t)
	dir := f.repo("loose")
	f.commit(dir, "a.txt", "a\n", "first commit")
	sameAsLog(t, f, dir, true)
	f.commit(dir, "b.txt", "b\n", "second commit with unicode: café ✓")
	sameAsLog(t, f, dir, true)
	f.write(dir, "c.txt", "c\n")
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-q", "--date=2020-01-02T03:04:05-0330", "-m", "odd zone   ", "-m", "body text")
	sameAsLog(t, f, dir, true)
	f.write(dir, "d.txt", "d\n")
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-q", "--date=2021-06-01T10:00:00+0000", "-m", "utc")
	sameAsLog(t, f, dir, true)
}

func TestReadCommitDirect_PackedMatchesLog(t *testing.T) {
	f := newFx(t)
	dir := f.repo("packed")
	// Long, similar messages so that git stores later commits as deltas.
	body := strings.Repeat("A long explanation that every commit of this repo repeats word for word.\n", 60)
	for i := range 8 {
		f.write(dir, "a.txt", strings.Repeat("line\n", 40+i))
		f.git(dir, "add", "-A")
		f.git(dir, "commit", "-q", "-m", "commit number "+strings.Repeat("x", i), "-m", body)
	}
	f.git(dir, "gc", "-q")
	if m, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "??", "*")); len(m) != 0 {
		t.Fatalf("gc left loose objects: %v", m)
	}
	sameAsLog(t, f, dir, true)

	// Walk every commit of a deltified pack, not only the tip.
	f.git(dir, "-c", "pack.depth=50", "-c", "pack.window=250", "gc", "-q", "--aggressive")
	for _, h := range strings.Fields(f.git(dir, "rev-list", "HEAD")) {
		want, err := f.g.LogRef(context.Background(), dir, h, 1)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := ReadCommitDirect(dir, h)
		if !ok || got.Subject != want[0].Subject || got.Author != want[0].Author {
			t.Errorf("%s: direct %+v ok=%v, git %+v", h, got, ok, want[0])
		}
	}
}

func TestReadCommitDirect_DefersToGit(t *testing.T) {
	f := newFx(t)
	mk := func(name string) (string, string) {
		dir := f.repo(name)
		f.commit(dir, "a.txt", "a\n", "one")
		return dir, strings.TrimSpace(f.git(dir, "rev-parse", "HEAD"))
	}

	t.Run("wrapped subject", func(t *testing.T) {
		dir := f.repo("wrapped")
		f.write(dir, "a.txt", "a\n")
		f.git(dir, "add", "-A")
		f.git(dir, "commit", "-q", "-F", writeMsg(t, "first line\nsecond line\n\nbody\n"))
		sameAsLog(t, f, dir, false)
	})
	t.Run("indented subject", func(t *testing.T) {
		dir := f.repo("indent")
		f.write(dir, "a.txt", "a\n")
		f.git(dir, "add", "-A")
		f.git(dir, "commit", "-q", "--cleanup=verbatim", "-F", writeMsg(t, "  indented\n"))
		sameAsLog(t, f, dir, false)
	})
	t.Run("encoding header", func(t *testing.T) {
		dir := f.repo("enc")
		f.write(dir, "a.txt", "a\n")
		f.git(dir, "add", "-A")
		f.git(dir, "-c", "i18n.commitEncoding=ISO-8859-1", "commit", "-q", "-m", "latin")
		sameAsLog(t, f, dir, false)
	})
	t.Run("unknown object", func(t *testing.T) {
		dir, _ := mk("unknown")
		if _, ok := ReadCommitDirect(dir, strings.Repeat("ab", 20)); ok {
			t.Error("answered for an object that is not there")
		}
	})
	t.Run("not a sha-1 id", func(t *testing.T) {
		dir, oid := mk("sha256")
		if _, ok := ReadCommitDirect(dir, oid+oid[:24]); ok {
			t.Error("answered for a 64-character id")
		}
		if _, ok := ReadCommitDirect(dir, "zz"+oid[2:]); ok {
			t.Error("answered for a non-hex id")
		}
	})
	t.Run("damaged loose object", func(t *testing.T) {
		dir, oid := mk("damaged")
		p := filepath.Join(dir, ".git", "objects", oid[:2], oid[2:])
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("not zlib"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadCommitDirect(dir, oid); ok {
			t.Error("answered from a damaged object")
		}
	})
	t.Run("object whose content does not match its id", func(t *testing.T) {
		dir, oid := mk("swapped")
		other := f.repo("other")
		f.commit(other, "z.txt", "z\n", "different")
		oo := strings.TrimSpace(f.git(other, "rev-parse", "HEAD"))
		src, err := os.ReadFile(filepath.Join(other, ".git", "objects", oo[:2], oo[2:]))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, ".git", "objects", oid[:2], oid[2:])
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, src, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadCommitDirect(dir, oid); ok {
			t.Error("answered with content that does not hash to the id")
		}
	})
	t.Run("alternates", func(t *testing.T) {
		dir, oid := mk("alt")
		if err := os.WriteFile(filepath.Join(dir, ".git", "objects", "info", "alternates"), []byte("../x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadCommitDirect(dir, oid); ok {
			t.Error("answered in a repo with alternates")
		}
	})
	t.Run("replace refs", func(t *testing.T) {
		dir, oid := mk("repl")
		f.commit(dir, "b.txt", "b\n", "two")
		f.git(dir, "replace", oid, "HEAD")
		if _, ok := ReadCommitDirect(dir, oid); ok {
			t.Error("answered in a repo with replace refs")
		}
	})
	t.Run("no .git folder", func(t *testing.T) {
		if _, ok := ReadCommitDirect(t.TempDir(), strings.Repeat("ab", 20)); ok {
			t.Error("answered outside a repo")
		}
	})
}

func writeMsg(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "msg")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
