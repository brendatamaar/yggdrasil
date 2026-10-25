package git

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// One case per rejection rule of the direct commit read. Every table has a
// control row that must be accepted, so a rule cannot "pass" because the
// fixture itself is broken.

func TestParseIdent_RejectionRules(t *testing.T) {
	cases := []struct {
		name, in string
		ok       bool
	}{
		{"control", "Ada Test <ada@example.test> 1700000000 +0100", true},
		{"no <", "Ada ada@example.test> 1700000000 +0100", false},
		{"< at the start, no name", "<ada@example.test> 1700000000 +0100", false},
		{"no space before <", "Ada<ada@example.test> 1700000000 +0100", false},
		{"no >", "Ada <ada@example.test 1700000000 +0100", false},
		{"> before <", "Ada >x< 1700000000 +0100", false},
		{"< inside email", "Ada <a <b@example.test> 1700000000 +0100", false},
		{"> inside name", "Ada> <ada@example.test> 1700000000 +0100", false},
		{"> inside email", "Ada <a>b@example.test> 1700000000 +0100", false},
		{"tz too short", "Ada <a@example.test> 1700000000 +010", false},
		{"tz too long", "Ada <a@example.test> 1700000000 +01000", false},
		{"tz without sign", "Ada <a@example.test> 1700000000 01000", false},
		{"tz with a bad sign", "Ada <a@example.test> 1700000000 *0100", false},
		{"tz with letters", "Ada <a@example.test> 1700000000 +01a0", false},
		{"timestamp not a number", "Ada <a@example.test> yesterday +0100", false},
		{"timestamp too big", "Ada <a@example.test> 99999999999999999999 +0100", false},
		{"no timestamp", "Ada <a@example.test> +0100", false},
		{"no tz", "Ada <a@example.test> 1700000000", false},
		{"extra field", "Ada <a@example.test> 1700000000 +0100 x", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := parseIdent(c.in)
			if (err == nil) != c.ok {
				t.Errorf("parseIdent(%q) err = %v, want accepted=%v", c.in, err, c.ok)
			}
		})
	}
}

func TestParseCommitObject_RejectionRules(t *testing.T) {
	oid := strings.Repeat("a", 40)
	cases := []struct {
		name string
		raw  []byte
		ok   bool
	}{
		{"control", commitRaw(goodAuthor, "", "subject\n"), true},
		{"empty message is fine", commitRaw(goodAuthor, "", ""), true},
		{"blank message is fine", commitRaw(goodAuthor, "", "  \n\n"), true},
		{"no author header", []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\ncommitter " + goodAuthor + "\n\nx\n"), false},
		{"bad author line", commitRaw("Ada Test 1700000000 +0100", "", "x\n"), false},
		{"encoding header", commitRaw(goodAuthor, "encoding ISO-8859-1\n", "x\n"), false},
		{"wrapped subject", commitRaw(goodAuthor, "", "first\nsecond\n\nbody\n"), false},
		{"indented subject", commitRaw(goodAuthor, "", "  indented\n"), false},
		{"subject starts with a tab", commitRaw(goodAuthor, "", "\tindented\n"), false},
		{"blank first line then text", commitRaw(goodAuthor, "", "\n\nlater\n"), false},
		{"no blank line at all", []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor " + goodAuthor + "\n"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseCommitObject(oid, c.raw)
			if (err == nil) != c.ok {
				t.Errorf("err = %v, want accepted=%v", err, c.ok)
			}
		})
	}
}

func TestReadLoose_RejectionRules(t *testing.T) {
	_, obj := fakeRepo(t)
	body := commitRaw(goodAuthor, "", "x\n")
	put := func(name string, stream []byte) string {
		oid := objID("commit", append([]byte(name), body...)) // any distinct id
		putLoose(t, obj, oid, stream)
		return oid
	}
	hdr := func(h string) []byte { return append([]byte(h+"\x00"), body...) }
	good := len(body)

	t.Run("control", func(t *testing.T) {
		oid := put("ok", hdr("commit "+itoa(good)))
		got, err := readLoose(obj, oid)
		if err != nil || string(got) != string(body) {
			t.Fatalf("err %v, body %q", err, got)
		}
	})
	rejected := map[string][]byte{
		"declared size smaller than the body": hdr("commit " + itoa(good-1)),
		"declared size bigger than the body":  hdr("commit " + itoa(good+1)),
		"size not a number":                   hdr("commit lots"),
		"size empty":                          hdr("commit "),
		"type is blob":                        hdr("blob " + itoa(good)),
		"no space after the type":             hdr("commit" + itoa(good)),
		"no NUL after the header":             append([]byte("commit "+itoa(good)+" "), body...),
		"negative size":                       hdr("commit -1"),
	}
	for name, stream := range rejected {
		t.Run(name, func(t *testing.T) {
			oid := put(name, stream)
			if _, err := readLoose(obj, oid); err == nil {
				t.Error("accepted")
			}
		})
	}
	t.Run("oversize but honest", func(t *testing.T) {
		big := make([]byte, maxCommitBytes+1)
		oid := put("big", append([]byte("commit "+itoa(len(big))+"\x00"), big...))
		if _, err := readLoose(obj, oid); err == nil {
			t.Error("accepted a commit above the size limit")
		}
	})
	t.Run("oversize beyond the read limit", func(t *testing.T) {
		big := make([]byte, maxCommitBytes+4096)
		oid := put("huge", append([]byte("commit "+itoa(len(big))+"\x00"), big...))
		if _, err := readLoose(obj, oid); err == nil {
			t.Error("accepted a commit above the size limit")
		}
	})
	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		big := make([]byte, maxCommitBytes)
		oid := put("edge", append([]byte("commit "+itoa(len(big))+"\x00"), big...))
		if _, err := readLoose(obj, oid); err != nil {
			t.Errorf("rejected a commit of exactly the limit: %v", err)
		}
	})
	t.Run("missing file is ErrNotExist", func(t *testing.T) {
		_, err := readLoose(obj, strings.Repeat("cd", 20))
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("err = %v, want ErrNotExist so packs are tried", err)
		}
	})
	t.Run("not zlib", func(t *testing.T) {
		oid := strings.Repeat("ef", 20)
		p := filepath.Join(obj, oid[:2], oid[2:])
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("plain text"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readLoose(obj, oid); err == nil {
			t.Error("accepted a file that is not zlib")
		}
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

// packCase writes a pack holding ents (the commit is the entry listed under
// its id) and reads it through readCommitDirect.
func packRead(t *testing.T, ents []packEnt, idxVersion uint32, oid string) (ok bool) {
	t.Helper()
	dir, obj := fakeRepo(t)
	writePack(t, obj, "case", ents, idxVersion)
	_, ok = readCommitDirect(dir, oid)
	return ok
}

func TestReadPacked_RejectionRules(t *testing.T) {
	raw := commitRaw(goodAuthor, "", "packed subject\n")
	oid := objID("commit", raw)
	sum := mustSum(oid)
	full := packEnt{typ: 1, data: raw, oid: oid}

	t.Run("control: full entry", func(t *testing.T) {
		if !packRead(t, []packEnt{full}, 2, oid) {
			t.Fatal("hand-built pack not read; the fixture is broken")
		}
	})
	t.Run("control: ofs delta", func(t *testing.T) {
		ents := []packEnt{
			{typ: 1, data: raw},
			{typ: 6, baseIdx: 0, data: copyAllDelta(len(raw)), oid: oid},
		}
		if !packRead(t, ents, 2, oid) {
			t.Fatal("ofs delta not read")
		}
	})
	t.Run("ref delta tip", func(t *testing.T) {
		other := commitRaw(goodAuthor, "", "other subject!\n")
		oo := objID("commit", other)
		ents := []packEnt{
			{typ: 1, data: raw, oid: oid},
			{typ: 7, baseSum: sum, data: mkDelta(len(raw), len(other), dInsert(other[:100]), dInsert(other[100:])), oid: oo},
		}
		if !packRead(t, ents, 2, oo) {
			t.Fatal("ref delta tip not read")
		}
	})

	t.Run("index version 1", func(t *testing.T) {
		if packRead(t, []packEnt{full}, 1, oid) {
			t.Error("accepted an index that is not version 2")
		}
	})
	t.Run("index version 3", func(t *testing.T) {
		if packRead(t, []packEnt{full}, 3, oid) {
			t.Error("accepted an index that is not version 2")
		}
	})
	t.Run("64-bit offset", func(t *testing.T) {
		e := full
		e.bigIdxOf = true
		if packRead(t, []packEnt{e}, 2, oid) {
			t.Error("accepted a 64-bit pack offset")
		}
	})
	t.Run("object not in the index", func(t *testing.T) {
		e := full
		e.oid = strings.Repeat("11", 20)
		if packRead(t, []packEnt{e}, 2, oid) {
			t.Error("answered for an id the index does not list")
		}
	})
	t.Run("entry is a tree", func(t *testing.T) {
		e := packEnt{typ: 2, data: raw, oid: oid}
		if packRead(t, []packEnt{e}, 2, oid) {
			t.Error("accepted a non-commit entry")
		}
	})
	t.Run("entry is a blob", func(t *testing.T) {
		e := packEnt{typ: 3, data: raw, oid: oid}
		if packRead(t, []packEnt{e}, 2, oid) {
			t.Error("accepted a non-commit entry")
		}
	})
	t.Run("entry is a tag", func(t *testing.T) {
		e := packEnt{typ: 4, data: raw, oid: oid}
		if packRead(t, []packEnt{e}, 2, oid) {
			t.Error("accepted a non-commit entry")
		}
	})
	t.Run("delta on a blob base yields a non-commit", func(t *testing.T) {
		ents := []packEnt{
			{typ: 3, data: raw},
			{typ: 6, baseIdx: 0, data: copyAllDelta(len(raw)), oid: oid},
		}
		if packRead(t, ents, 2, oid) {
			t.Error("accepted a delta whose base is a blob")
		}
	})
	t.Run("content does not hash to the id", func(t *testing.T) {
		e := packEnt{typ: 1, data: commitRaw(goodAuthor, "", "other\n"), oid: oid}
		if packRead(t, []packEnt{e}, 2, oid) {
			t.Error("accepted pack content that does not hash to the id")
		}
	})
	t.Run("entry size in the header is wrong", func(t *testing.T) {
		e := full
		e.sizeLie = 3
		if packRead(t, []packEnt{e}, 2, oid) {
			t.Error("accepted an entry whose inflated size differs from its header")
		}
	})
	t.Run("entry size over the limit", func(t *testing.T) {
		e := full
		e.sizeLie = 5 << 20
		if packRead(t, []packEnt{e}, 2, oid) {
			t.Error("accepted an entry above the size limit")
		}
	})
	t.Run("delta base size mismatch", func(t *testing.T) {
		ents := []packEnt{
			{typ: 1, data: raw},
			{typ: 6, baseIdx: 0, data: mkDelta(len(raw)+1, len(raw), dCopy(0, len(raw))), oid: oid},
		}
		if packRead(t, ents, 2, oid) {
			t.Error("accepted a delta that declares another base size")
		}
	})
	t.Run("delta copy out of range", func(t *testing.T) {
		ents := []packEnt{
			{typ: 1, data: raw},
			{typ: 6, baseIdx: 0, data: mkDelta(len(raw), len(raw), dCopy(1, len(raw))), oid: oid},
		}
		if packRead(t, ents, 2, oid) {
			t.Error("accepted a copy beyond the base")
		}
	})
	t.Run("delta result shorter than declared", func(t *testing.T) {
		ents := []packEnt{
			{typ: 1, data: raw},
			{typ: 6, baseIdx: 0, data: mkDelta(len(raw), len(raw)+5, dCopy(0, len(raw))), oid: oid},
		}
		if packRead(t, ents, 2, oid) {
			t.Error("accepted a delta that makes less than it declares")
		}
	})
	t.Run("ofs delta points before the pack", func(t *testing.T) {
		ents := []packEnt{
			{typ: 1, data: raw},
			{typ: 6, baseIdx: 0, relForce: 1 << 20, data: copyAllDelta(len(raw)), oid: oid},
		}
		if packRead(t, ents, 2, oid) {
			t.Error("accepted a delta base before the start of the pack")
		}
	})
	t.Run("ref delta with an unknown base", func(t *testing.T) {
		ents := []packEnt{
			{typ: 7, baseSum: mustSum(strings.Repeat("22", 20)), data: copyAllDelta(len(raw)), oid: oid},
		}
		if packRead(t, ents, 2, oid) {
			t.Error("accepted a ref delta whose base is not in the index")
		}
	})
	t.Run("ref delta loop", func(t *testing.T) {
		// The only entry is its own base: the chain never ends, so the depth
		// limit has to stop it.
		ents := []packEnt{
			{typ: 7, baseSum: sum, data: copyAllDelta(len(raw)), oid: oid},
		}
		if packRead(t, ents, 2, oid) {
			t.Error("accepted a delta that is its own base")
		}
	})
	t.Run("ofs delta depth boundary", func(t *testing.T) {
		chain := func(deltas int) []packEnt {
			ents := []packEnt{{typ: 1, data: raw}}
			for i := range deltas {
				e := packEnt{typ: 6, baseIdx: i, data: copyAllDelta(len(raw))}
				if i == deltas-1 {
					e.oid = oid
				}
				ents = append(ents, e)
			}
			return ents
		}
		if !packRead(t, chain(maxDeltaDepth), 2, oid) {
			t.Errorf("a chain of %d deltas (the maximum) was rejected", maxDeltaDepth)
		}
		if packRead(t, chain(maxDeltaDepth+1), 2, oid) {
			t.Errorf("a chain of %d deltas was accepted", maxDeltaDepth+1)
		}
	})
	t.Run("inflate budget shared by the chain", func(t *testing.T) {
		// Each entry is small, together they exceed the budget: a 3 MiB commit
		// body per entry, three entries deep.
		big := commitRaw(goodAuthor, "", "big\n"+strings.Repeat("x", 900<<10))
		bo := objID("commit", big)
		ents := []packEnt{{typ: 1, data: big}}
		for i := range 5 {
			e := packEnt{typ: 6, baseIdx: i, data: litDelta(len(big), big)}
			if i == 4 {
				e.oid = bo
			}
			ents = append(ents, e)
		}
		// 6 entries x 2 x ~0.9 MiB = ~10.8 MiB > 8 MiB budget.
		if packRead(t, ents, 2, bo) {
			t.Error("accepted a chain that exceeds the shared inflate budget")
		}
	})
	t.Run("pack file missing", func(t *testing.T) {
		dir, obj := fakeRepo(t)
		writePack(t, obj, "case", []packEnt{full}, 2)
		if err := os.Rename(filepath.Join(obj, "pack", "pack-case.pack"), filepath.Join(obj, "pack", "pack-case.gone")); err != nil {
			t.Fatal(err)
		}
		if _, ok := readCommitDirect(dir, oid); ok {
			t.Error("answered without the pack file")
		}
	})
	t.Run("truncated pack", func(t *testing.T) {
		dir, obj := fakeRepo(t)
		offs := writePack(t, obj, "case", []packEnt{full}, 2)
		p := filepath.Join(obj, "pack", "pack-case.pack")
		if err := os.Truncate(p, offs[0]+3); err != nil {
			t.Fatal(err)
		}
		if _, ok := readCommitDirect(dir, oid); ok {
			t.Error("answered from a truncated pack")
		}
	})
}

func mustSum(oid string) []byte {
	b := make([]byte, 20)
	for i := range b {
		b[i] = hexByte(oid[2*i : 2*i+2])
	}
	return b
}

func TestReadCommitDirect_GitFileWorktreeFallsBack(t *testing.T) {
	f := newFx(t)
	main := f.repo("main-checkout")
	f.commit(main, "a.txt", "a\n", "first")
	wt := filepath.Join(t.TempDir(), "linked wt") // a path with a space, too
	f.git(main, "worktree", "add", "-q", "-b", "side", wt)
	fi, err := os.Lstat(filepath.Join(wt, ".git"))
	if err != nil || fi.IsDir() {
		t.Fatalf("expected a .git file in the linked worktree: %v %v", fi, err)
	}
	sameAsLog(t, f, wt, false)
	// The same commit through the main checkout is answered.
	sameAsLog(t, f, main, true)
}

func TestReadCommitDirect_PathWithSpaces(t *testing.T) {
	f := newFx(t)
	dir := filepath.Join(t.TempDir(), "acme shop", "shop api")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	f.git(dir, "init", "-q", "-b", "main")
	f.commit(dir, "a.txt", "a\n", "spaces in the path")
	sameAsLog(t, f, dir, true)
}

// A repo deeper than MAX_PATH (260) on Windows. git cannot even be started
// there (the working directory limit), so the repo is written by hand.
func TestReadCommitDirect_LongPath(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 300 {
		dir = filepath.Join(dir, "deep-folder-for-the-long-path-case")
	}
	objDir := filepath.Join(dir, ".git", "objects")
	if err := os.MkdirAll(objDir, 0o750); err != nil {
		t.Skipf("long paths cannot be created here: %v", err)
	}
	oid := putCommit(t, objDir, commitRaw(goodAuthor, "", "deep subject\n"))
	c, ok := readCommitDirect(dir, oid)
	if !ok || c.Subject != "deep subject" {
		t.Errorf("long path: ok=%v commit=%+v", ok, c)
	}
}
