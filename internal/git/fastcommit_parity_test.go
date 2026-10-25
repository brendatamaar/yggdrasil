package git

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity matrix: for each odd commit shape the direct read must either give
// the same answer as `git log -1` or decline (ok=false). A wrong answer is the
// only failure. The test also counts how many shapes were answered, so a
// regression that makes the direct read decline everything is visible.

// storeRaw writes raw as a commit object in dir with git (--literally: git
// does not check the shape, so deliberately odd commits are possible).
func (f *fx) storeRaw(dir string, raw []byte) string {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), "obj")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(f.git(dir, "hash-object", "-t", "commit", "-w", "--literally", p))
}

// parity compares the direct read of oid with git log. It returns whether the
// direct read answered.
func parity(t *testing.T, f *fx, dir, oid string) (answered bool) {
	t.Helper()
	want, err := f.g.LogRef(context.Background(), dir, oid, 1)
	if err != nil || len(want) != 1 {
		t.Fatalf("git log %s: %v %v", oid, want, err)
	}
	w := want[0]
	got, ok := ReadCommitDirect(dir, oid)
	if !ok {
		return false
	}
	gd, wd := got.Date.Format("2006-01-02T15:04:05Z07:00"), w.Date.Format("2006-01-02T15:04:05Z07:00")
	if got.Hash != w.Hash || got.Short != w.Short || got.Author != w.Author || got.Email != w.Email ||
		got.Subject != w.Subject || gd != wd || !got.Date.Equal(w.Date) ||
		strings.Join(got.Parents, " ") != strings.Join(w.Parents, " ") {
		t.Errorf("wrong answer for %s\n  direct = %+v (%s)\n  git    = %+v (%s)", oid, got, gd, w, wd)
	}
	return true
}

func (f *fx) emptyTree(dir string) string {
	return strings.TrimSpace(f.git(dir, "write-tree"))
}

func TestParity_RealCommits(t *testing.T) {
	f := newFx(t)
	dir := f.repo("parity-real")
	tree := f.emptyTree(dir)
	msg := func(s string) string {
		p := filepath.Join(t.TempDir(), "m")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ct := func(parents []string, message string) string {
		args := []string{"commit-tree", tree}
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		args = append(args, "-F", msg(message))
		return strings.TrimSpace(f.git(dir, args...))
	}
	root := ct(nil, "root commit\n")
	a := ct([]string{root}, "second\n\nbody line\n")
	b := ct([]string{root}, "side branch\n")
	merge := ct([]string{a, b}, "Merge side into main\n")
	octopus := ct([]string{root, a, b}, "octopus\n")
	emptyMsg := ct([]string{root}, "")
	tabCR := ct([]string{root}, "subject with trailing tab\t\r\n")
	crlf := ct([]string{root}, "crlf subject\r\n\r\nbody one\r\nbody two\r\n")
	unicode := ct([]string{root}, "unicode subject: café ✓ 日本語\n")
	long := ct([]string{root}, strings.Repeat("long ", 2000)+"\n")

	answered := 0
	for name, oid := range map[string]string{
		"root": root, "child": a, "merge (2 parents)": merge, "octopus (3 parents)": octopus,
		"empty message": emptyMsg, "trailing tab and CR": tabCR, "CRLF message": crlf,
		"unicode subject": unicode, "10k subject": long,
	} {
		t.Run(name, func(t *testing.T) {
			if parity(t, f, dir, oid) {
				answered++
			}
		})
	}
	// Merge and root are the shapes that decide the Parents field.
	if answered < 6 {
		t.Errorf("the direct read answered only %d of 9 plain shapes; it should handle most", answered)
	}
}

func TestParity_RawShapes(t *testing.T) {
	f := newFx(t)
	dir := f.repo("parity-raw")
	root := f.storeRaw(dir, commitRaw(goodAuthor, "", "root\n"))
	sig := "gpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEE\n =abcd\n -----END PGP SIGNATURE-----\n"
	tag := "mergetag object " + root + "\n type commit\n tag v1\n tagger Ada Test <ada@example.test> 1700000000 +0100\n \n release\n"
	cases := []struct {
		name   string
		author string
		extra  string
		msg    string
	}{
		{"plain", goodAuthor, "", "plain\n"},
		{"gpgsig header", goodAuthor, sig, "signed\n"},
		{"mergetag header", goodAuthor, tag, "merge with tag\n"},
		{"gpgsig and mergetag", goodAuthor, tag + sig, "both\n"},
		{"unicode author", "Zoë Ünï 日本 <zoe@example.test> 1700000000 +0100", "", "u\n"},
		{"negative tz", "Ada Test <a@example.test> 1700000000 -0330", "", "neg\n"},
		{"tz +0000", "Ada Test <a@example.test> 1700000000 +0000", "", "z\n"},
		{"tz -0000", "Ada Test <a@example.test> 1700000000 -0000", "", "nz\n"},
		{"tz +1400", "Ada Test <a@example.test> 1700000000 +1400", "", "max\n"},
		{"tz -1200", "Ada Test <a@example.test> 1700000000 -1200", "", "min\n"},
		{"tz +0545", "Ada Test <a@example.test> 1700000000 +0545", "", "nepal\n"},
		{"timestamp 0", "Ada Test <a@example.test> 0 +0000", "", "epoch\n"},
		{"timestamp 2100", "Ada Test <a@example.test> 4102444800 +0100", "", "future\n"},
		{"empty name", " <a@example.test> 1700000000 +0100", "", "noname\n"},
		{"empty email", "Ada Test <> 1700000000 +0100", "", "noemail\n"},
		{"email with spaces", "Ada Test <a b@example.test> 1700000000 +0100", "", "sp\n"},
		{"name with tab", "Ada\tTest <a@example.test> 1700000000 +0100", "", "tab\n"},
		{"empty message", goodAuthor, "", ""},
		{"message only newline", goodAuthor, "", "\n"},
		{"whitespace message", goodAuthor, "", "   \n\t\n"},
		{"subject trailing spaces", goodAuthor, "", "trailing   \n"},
		{"subject trailing tab", goodAuthor, "", "trailing\t\n"},
		{"subject trailing CR", goodAuthor, "", "trailing\r\n"},
		{"subject CR inside", goodAuthor, "", "a\rb\n"},
		{"CRLF body", goodAuthor, "", "s\r\n\r\nb\r\n"},
		{"two blank lines then body", goodAuthor, "", "s\n\n\nbody\n"},
		{"no trailing newline", goodAuthor, "", "no newline"},
		{"body only after blank", goodAuthor, "", "\nbody first\n"},
		{"latin-1 bytes in subject", goodAuthor, "", "caf\xe9\n"},
		{"encoding header", goodAuthor, "encoding ISO-8859-1\n", "latin\n"},
		{"wrapped subject", goodAuthor, "", "one\ntwo\n\nthree\n"},
		{"subject looks like a header", goodAuthor, "", "author Mallory <m@example.test> 1 +0000\n"},
		{"body looks like a header", goodAuthor, "", "s\n\nauthor Mallory <m@example.test> 1 +0000\nparent " + root + "\n"},
		{"double space before tz", "Ada Test <a@example.test> 1700000000  +0100", "", "ds\n"},
	}
	answered := 0
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oid := f.storeRaw(dir, commitRaw(c.author, c.extra, c.msg, root))
			if parity(t, f, dir, oid) {
				answered++
			}
		})
	}
	t.Run("no message separator", func(t *testing.T) {
		raw := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor " + goodAuthor + "\ncommitter " + goodAuthor + "\n")
		parity(t, f, dir, f.storeRaw(dir, raw))
	})
	t.Run("author after committer", func(t *testing.T) {
		raw := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ncommitter " + goodAuthor + "\nauthor " + goodAuthor + "\n\nreordered\n")
		parity(t, f, dir, f.storeRaw(dir, raw))
	})
	if answered < 15 {
		t.Errorf("the direct read answered only %d shapes; it should handle the plain ones", answered)
	}
}

// Randomised differential test with a fixed seed: when the direct read
// accepts a commit, it equals git log -1.
func TestParity_RandomCommits(t *testing.T) {
	f := newFx(t)
	dir := f.repo("parity-random")
	rng := rand.New(rand.NewSource(20240601))
	names := []string{"Ada Test", "Zoë Ünï", "日本語 名前", "A", "Bob  Two Spaces", "Dr. O'Neil-Smith", "x y z"}
	emails := []string{"ada@example.test", "z@x.test", "", "a+b@example.test", "weird email@example.test"}
	subjects := []string{"fix cart total", "feat(api): add /v2/orders", "trailing  ", "trailing\t", "trailing\r", "café ✓", "a", "",
		"  leading", "\tleading tab", "Merge branch 'main' into feature/x", strings.Repeat("w", 300), "100% done #12", "a\\b \"q\" 'q'"}
	bodies := []string{"", "\nbody\n", "\n\nbody\n", "\r\nbody\r\n", "\nline1\nline2\n", "\n\n\n\n", "\n \n", "\nsigned-off-by: Ada Test <ada@example.test>\n"}
	tzs := []string{"+0000", "-0000", "+0100", "-0330", "+0545", "+1400", "-1200", "+0930", "-0001"}
	extras := []string{"", "", "", "gpgsig -----BEGIN-----\n x\n -----END-----\n", "encoding UTF-8\n", "encoding ISO-8859-1\n", "x-custom header value\n"}
	var oids []string
	answered := 0
	const n = 30
	for i := range n {
		var ps []string
		for k := rng.Intn(3); k > 0 && len(oids) > 0; k-- {
			ps = append(ps, oids[rng.Intn(len(oids))])
		}
		author := fmt.Sprintf("%s <%s> %d %s", names[rng.Intn(len(names))], emails[rng.Intn(len(emails))], rng.Int63n(4_000_000_000), tzs[rng.Intn(len(tzs))])
		subj := subjects[rng.Intn(len(subjects))]
		msg := subj + "\n" + bodies[rng.Intn(len(bodies))]
		if rng.Intn(12) == 0 {
			msg = subj // no trailing newline
		}
		raw := commitRaw(author, extras[rng.Intn(len(extras))], msg, ps...)
		oid := f.storeRaw(dir, raw)
		oids = append(oids, oid)
		t.Run(fmt.Sprintf("case %02d", i), func(t *testing.T) {
			if parity(t, f, dir, oid) {
				answered++
			}
		})
	}
	if answered < n/3 {
		t.Errorf("direct read answered only %d of %d random commits", answered, n)
	}
	t.Logf("direct read answered %d of %d random commits; the rest fell back to git", answered, n)
}

// packObjectTypes returns the entry type of every object of a pack index.
func packObjectTypes(t *testing.T, idxPath string) map[int]int {
	t.Helper()
	idx, err := openIndex(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.f.Close()
	pack, err := os.Open(strings.TrimSuffix(idxPath, ".idx") + ".pack")
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	offTab := int64(8+1024) + idx.n*20 + idx.n*4
	types := map[int]int{}
	for i := range idx.n {
		var b [4]byte
		if _, err := idx.f.ReadAt(b[:], offTab+i*4); err != nil {
			t.Fatal(err)
		}
		off := int64(binary.BigEndian.Uint32(b[:]) & 0x7fffffff)
		var h [1]byte
		if _, err := pack.ReadAt(h[:], off); err != nil {
			t.Fatal(err)
		}
		types[int(h[0]>>4)&7]++
	}
	return types
}

func deltaHistory(t *testing.T, f *fx, dir string, n int) {
	t.Helper()
	body := strings.Repeat("A long explanation that every commit of this repo repeats word for word.\n", 60)
	for i := range n {
		f.write(dir, "a.txt", strings.Repeat("line\n", 40+i))
		f.git(dir, "add", "-A")
		f.git(dir, "commit", "-q", "-m", "commit number "+strings.Repeat("x", i), "-m", body)
	}
}

func parityAll(t *testing.T, f *fx, dir string) (answered, total int) {
	t.Helper()
	for _, h := range strings.Fields(f.git(dir, "rev-list", "--all")) {
		total++
		if parity(t, f, dir, h) {
			answered++
		}
	}
	return answered, total
}

func TestParity_RefDeltaPackFromGit(t *testing.T) {
	f := newFx(t)
	dir := f.repo("parity-refdelta")
	deltaHistory(t, f, dir, 10)
	f.git(dir, "-c", "repack.useDeltaBaseOffset=false", "-c", "pack.depth=50", "-c", "pack.window=250", "gc", "-q", "--aggressive")
	idxs, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "pack-*.idx"))
	refDeltas := 0
	for _, p := range idxs {
		refDeltas += packObjectTypes(t, p)[7]
	}
	if refDeltas == 0 {
		t.Skip("this git version wrote no REF_DELTA entries")
	}
	answered, total := parityAll(t, f, dir)
	if answered == 0 {
		t.Errorf("none of %d commits in a REF_DELTA pack was answered directly", total)
	}
}

// A pack written by hand holds a REF_DELTA entry; real git reads the same
// pack, so both can be compared.
func TestParity_HandBuiltRefDeltaPackMatchesGit(t *testing.T) {
	f := newFx(t)
	dir := f.repo("parity-handpack")
	obj := filepath.Join(dir, ".git", "objects")
	base := commitRaw(goodAuthor, "", "base commit with a fairly long subject line here\n")
	tipRaw := commitRaw("Zoë Ünï <z@example.test> 1700000100 -0330", "", "tip commit made from the base\n")
	bo, to := objID("commit", base), objID("commit", tipRaw)
	delta := mkDelta(len(base), len(tipRaw), dInsert(tipRaw[:100]), dInsert(tipRaw[100:]))
	ents := []packEnt{
		{typ: 1, data: base, oid: bo},
		{typ: 7, baseSum: mustSum(bo), data: delta, oid: to},
	}
	writePack(t, obj, "hand", ents, 2)
	if strings.TrimSpace(f.git(dir, "cat-file", "-t", to)) != "commit" {
		t.Fatal("git does not read the hand-built pack")
	}
	for _, oid := range []string{bo, to} {
		if !parity(t, f, dir, oid) {
			t.Errorf("hand-built pack object %s was not answered directly", oid)
		}
	}
}

func TestParity_MultiPackRepo(t *testing.T) {
	f := newFx(t)
	dir := f.repo("parity-multipack")
	for i := range 3 {
		deltaHistory(t, f, dir, 3)
		_ = i
		f.git(dir, "gc", "-q")
	}
	idxs, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "pack-*.idx"))
	if len(idxs) < 2 {
		t.Skipf("gc made %d pack(s), wanted several", len(idxs))
	}
	answered, total := parityAll(t, f, dir)
	if answered != total {
		t.Errorf("answered %d of %d commits across %d packs; every commit is in some pack", answered, total, len(idxs))
	}
}

func TestParity_LooseAndPackedMixed(t *testing.T) {
	f := newFx(t)
	dir := f.repo("parity-mixed")
	f.commit(dir, "a.txt", "a\n", "packed one")
	f.git(dir, "gc", "-q")
	f.commit(dir, "b.txt", "b\n", "loose two")
	answered, total := parityAll(t, f, dir)
	if answered != total {
		t.Errorf("answered %d of %d", answered, total)
	}
}
