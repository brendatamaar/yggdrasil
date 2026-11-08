package builds

import (
	"bytes"
	"io/fs"
	"math/rand/v2"
	"reflect"
	"testing"
	"testing/fstest"
)

func depsRepo() fstest.MapFS {
	return fstest.MapFS{
		"package.json":      {Data: []byte(`{"name":"acme-shop-web"}`)},
		"package-lock.json": {Data: []byte("lock v1")},
	}
}

func TestHashDepsDeterministicAndHex(t *testing.T) {
	files := []string{"package.json", "package-lock.json"}
	a, b := HashDeps(depsRepo(), files), HashDeps(depsRepo(), files)
	if a != b || len(a) != 64 {
		t.Fatalf("%q %q", a, b)
	}
}

func TestHashDepsIgnoresOrderAndDuplicates(t *testing.T) {
	r := depsRepo()
	want := HashDeps(r, []string{"package-lock.json", "package.json"})
	for _, files := range [][]string{
		{"package.json", "package-lock.json"},
		{"package.json", "package-lock.json", "package.json"},
	} {
		if got := HashDeps(r, files); got != want {
			t.Errorf("%v: hash differs", files)
		}
	}
	in := []string{"b", "a"}
	HashDeps(r, in)
	if !reflect.DeepEqual(in, []string{"b", "a"}) {
		t.Fatal("HashDeps reordered its input")
	}
}

func TestHashDepsChangesWithContentAndNames(t *testing.T) {
	files := []string{"package.json", "package-lock.json"}
	base := HashDeps(depsRepo(), files)
	r := depsRepo()
	r["package-lock.json"] = &fstest.MapFile{Data: []byte("lock v2")}
	if HashDeps(r, files) == base {
		t.Error("content change not detected")
	}
	if HashDeps(depsRepo(), files[:1]) == base {
		t.Error("a missing name not detected")
	}
	// Moving bytes between name and content must not collide.
	x := fstest.MapFS{"a": {Data: []byte("bc")}, "ab": {Data: []byte("c")}}
	if HashDeps(x, []string{"a"}) == HashDeps(x, []string{"ab"}) {
		t.Error("name/content boundary collision")
	}
	y := fstest.MapFS{"a": {Data: []byte("x")}, "b": {Data: []byte("y")}}
	z := fstest.MapFS{"a": {Data: []byte("xb")}, "b": {Data: []byte("y")}}
	if HashDeps(y, []string{"a", "b"}) == HashDeps(z, []string{"a", "b"}) {
		t.Error("content change across files not detected")
	}
}

func TestHashDepsMissingAndOversizedGetMarkers(t *testing.T) {
	big := bytes.Repeat([]byte("x"), maxDepsFile+1)
	atLimit := bytes.Repeat([]byte("x"), maxDepsFile)
	r := fstest.MapFS{"big": {Data: big}, "limit": {Data: atLimit}, "small": {Data: []byte("x")}}
	// No panic, no error, and a stable hash.
	h1 := HashDeps(r, []string{"big", "missing", "limit"})
	if h1 != HashDeps(r, []string{"missing", "limit", "big"}) {
		t.Fatal("unstable")
	}
	// An oversized file counts as a marker: changing its content does not matter.
	r2 := fstest.MapFS{"big": {Data: append(bytes.Repeat([]byte("y"), maxDepsFile), 'z')}, "limit": {Data: atLimit}}
	if HashDeps(r2, []string{"big", "missing", "limit"}) != h1 {
		t.Error("an oversized file's content leaked into the hash")
	}
	// A file at exactly the limit is read.
	r3 := fstest.MapFS{"limit": {Data: append(bytes.Repeat([]byte("y"), maxDepsFile-1), 'z')}}
	if HashDeps(r3, []string{"limit"}) == HashDeps(fstest.MapFS{"limit": {Data: atLimit}}, []string{"limit"}) {
		t.Error("a file at the limit was not read")
	}
	// A marker differs from an empty file.
	if HashDeps(fstest.MapFS{"f": {}}, []string{"f"}) == HashDeps(fstest.MapFS{}, []string{"f"}) {
		t.Error("missing and empty collide")
	}
}

type errFS struct{}

func (errFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func TestHashDepsUnreadableIsNotAnError(t *testing.T) {
	if h := HashDeps(errFS{}, []string{"package.json"}); len(h) != 64 {
		t.Fatalf("%q", h)
	}
	if h := HashDeps(depsRepo(), nil); len(h) != 64 {
		t.Fatalf("empty list: %q", h)
	}
}

type fakeTC struct{ Toolchain }
type fakeDepsTC struct{ fakeTC }

func (fakeDepsTC) DepsFiles(Detection) []string { return []string{"a"} }

func TestDepsFilesHelper(t *testing.T) {
	if got := DepsFiles(fakeTC{}, Detection{}); got != nil {
		t.Fatalf("%v", got)
	}
	got := DepsFiles(fakeDepsTC{}, Detection{})
	got[0] = "changed"
	if again := DepsFiles(fakeDepsTC{}, Detection{}); again[0] != "a" {
		t.Fatal("not a copy")
	}
}

func FuzzHashDeps(f *testing.F) {
	f.Add("a", "x", "b", "y", uint8(0))
	f.Add("", "", "../x", "\x00", uint8(1))
	f.Fuzz(func(t *testing.T, n1, c1, n2, c2 string, seed uint8) {
		r := fstest.MapFS{}
		for n, c := range map[string]string{n1: c1, n2: c2} {
			if fs.ValidPath(n) && n != "." {
				r[n] = &fstest.MapFile{Data: []byte(c)}
			}
		}
		files := []string{n1, n2, "package.json"}
		h := HashDeps(r, files)
		rnd := rand.New(rand.NewPCG(uint64(seed), 1))
		rnd.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
		if h2 := HashDeps(r, files); h2 != h || len(h) != 64 {
			t.Fatalf("not stable under order: %q vs %q", h, h2)
		}
	})
}
