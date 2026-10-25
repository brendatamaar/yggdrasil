package git

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// packedRepo makes a repo whose commits are all packed.
func packedRepo(t *testing.T, f *fx, name string) (dir, tip string) {
	t.Helper()
	dir = f.repo(name)
	body := strings.Repeat("A long explanation that every commit of this repo repeats word for word.\n", 60)
	for i := range 6 {
		f.write(dir, "a.txt", strings.Repeat("line\n", 40+i))
		f.git(dir, "add", "-A")
		f.git(dir, "commit", "-q", "-m", "commit number "+strings.Repeat("x", i), "-m", body)
	}
	f.git(dir, "-c", "pack.depth=50", "-c", "pack.window=250", "gc", "-q", "--aggressive")
	return dir, strings.TrimSpace(f.git(dir, "rev-parse", "HEAD"))
}

func TestApplyDelta_RejectsRepeatedCopiesBeyondResultSize(t *testing.T) {
	const blk = 0x10000
	base := make([]byte, blk)
	// base size 65536, result size 65536, then 300 copies of the whole base
	// (op 0x80 with no size bytes means 0x10000).
	delta := []byte{0x80, 0x80, 0x04, 0x80, 0x80, 0x04}
	for range 300 {
		delta = append(delta, 0x80)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := applyDelta(base, delta)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("applyDelta accepted copies beyond the result size")
	}
	// The result buffer is 64 KiB; without the guard 300 copies allocate ~19 MiB.
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 1<<20 {
		t.Errorf("applyDelta allocated %d bytes before rejecting", grown)
	}
}

func TestApplyDelta_RejectsLiteralBeyondResultSize(t *testing.T) {
	base := []byte("abc")
	// base size 3, result size 2, but a 3 byte literal insert.
	delta := []byte{3, 2, 3, 'x', 'y', 'z'}
	if _, err := applyDelta(base, delta); err == nil {
		t.Fatal("literal insert beyond the result size accepted")
	}
}

func TestReadPackEntry_SharedBudget(t *testing.T) {
	f := newFx(t)
	dir, tip := packedRepo(t, f, "budget")
	idxs, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "pack-*.idx"))
	if len(idxs) != 1 {
		t.Fatalf("want one pack, got %v", idxs)
	}
	idx, err := openIndex(idxs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer idx.f.Close()
	sum := make([]byte, 20)
	for i := range sum {
		sum[i] = hexByte(tip[2*i : 2*i+2])
	}
	off, ok, err := idx.find(sum)
	if err != nil || !ok {
		t.Fatalf("find: %v %v", ok, err)
	}
	pack, err := os.Open(strings.TrimSuffix(idxs[0], ".idx") + ".pack")
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	big := int64(maxInflateBudget)
	if _, _, err := readPackEntry(pack, idx, off, 0, &big); err != nil {
		t.Fatalf("full budget: %v", err)
	}
	small := int64(16)
	if _, _, err := readPackEntry(pack, idx, off, 0, &small); err == nil {
		t.Fatal("a 16 byte budget was not enforced")
	}
}

func hexByte(s string) byte {
	var v byte
	for _, c := range s {
		v <<= 4
		switch {
		case c >= 'a':
			v |= byte(c-'a') + 10
		default:
			v |= byte(c - '0')
		}
	}
	return v
}

func TestReadCommitDirect_FallsBackOnShallowAndGrafts(t *testing.T) {
	f := newFx(t)
	dir := f.repo("shallow")
	f.commit(dir, "a.txt", "a\n", "first")
	f.commit(dir, "b.txt", "b\n", "second")
	sameAsLog(t, f, dir, true)

	cases := map[string]string{
		"shallow":     filepath.Join(dir, ".git", "shallow"),
		"info/grafts": filepath.Join(dir, ".git", "info", "grafts"),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(p)
			oid := strings.TrimSpace(f.git(dir, "rev-parse", "HEAD"))
			if _, ok := readCommitDirect(dir, oid); ok {
				t.Errorf("answered although %s exists", name)
			}
		})
	}
}

func TestReadCommitDirect_FallsBackOnSymlinkedGitOrObjects(t *testing.T) {
	f := newFx(t)
	for _, which := range []string{".git", "objects"} {
		t.Run(which, func(t *testing.T) {
			dir := f.repo("link-" + strings.TrimPrefix(which, "."))
			f.commit(dir, "a.txt", "a\n", "first")
			oid := strings.TrimSpace(f.git(dir, "rev-parse", "HEAD"))
			if _, ok := readCommitDirect(dir, oid); !ok {
				t.Fatal("plain repo not answered")
			}
			real := filepath.Join(dir, ".git")
			link := real
			if which == "objects" {
				link = filepath.Join(real, "objects")
			}
			moved := link + "-real"
			if err := os.Rename(link, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, link); err != nil {
				t.Skipf("symlinks cannot be created here: %v", err)
			}
			if _, ok := readCommitDirect(dir, oid); ok {
				t.Errorf("answered through a symlinked %s", which)
			}
		})
	}
}

func TestReadObject_TooManyPacksDefersToGit(t *testing.T) {
	f := newFx(t)
	dir, tip := packedRepo(t, f, "manypacks")
	if _, ok := readCommitDirect(dir, tip); !ok {
		t.Fatal("packed commit not answered")
	}
	pk := filepath.Join(dir, ".git", "objects", "pack")
	for i := range maxPacks + 1 {
		name := filepath.Join(pk, "pack-"+strings.Repeat("0", 30)+strings.Repeat("a", 4)+string(rune('a'+i/26))+string(rune('a'+i%26))+"zz.idx")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := readCommitDirect(dir, tip); ok {
		t.Error("answered with more than the maximum number of packs")
	}
}

func TestPackIndexFind_RejectsFanoutBeyondCount(t *testing.T) {
	buf := make([]byte, 8+1024+40000)
	binary.BigEndian.PutUint32(buf[0:], 0xff744f63)
	binary.BigEndian.PutUint32(buf[4:], 2)
	for i := range 256 { // fan-out says one object in total...
		v := uint32(1)
		if i < 0xab {
			v = 0
		}
		binary.BigEndian.PutUint32(buf[8+i*4:], v)
	}
	// ...but the entry for 0xab claims 1000.
	binary.BigEndian.PutUint32(buf[8+0xab*4:], 1000)
	p := filepath.Join(t.TempDir(), "pack-x.idx")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := openIndex(p)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.f.Close()
	sum := make([]byte, 20)
	sum[0] = 0xab
	if _, ok, err := idx.find(sum); err == nil || ok {
		t.Errorf("find on a damaged fan-out: ok=%v err=%v", ok, err)
	}
}

func TestReadCommitDirect_ShortMatchesLog(t *testing.T) {
	f := newFx(t)
	dir := f.repo("short")
	f.commit(dir, "a.txt", "a\n", "first")
	cs, err := f.g.Log(context.Background(), dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := readCommitDirect(dir, cs[0].Hash)
	if !ok {
		t.Fatal("not answered")
	}
	if got.Short != cs[0].Short {
		t.Errorf("Short = %q, git log shows %q", got.Short, cs[0].Short)
	}
}
