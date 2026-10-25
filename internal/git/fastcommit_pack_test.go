package git

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1" //nolint:gosec // git object ids
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Helpers that write git objects by hand: loose objects, pack files and
// version 2 pack indexes. They let the tests build exactly the damaged or odd
// shapes the direct commit read must reject, without a git process.

// objID returns the SHA-1 object id of content with the given type name.
func objID(typ string, content []byte) string {
	h := sha1.New() //nolint:gosec // git object ids
	fmt.Fprintf(h, "%s %d\x00", typ, len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// zlibBytes compresses b.
func zlibBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

// fakeRepo makes the directory skeleton readCommitDirect needs, without git.
func fakeRepo(t *testing.T) (dir, objDir string) {
	t.Helper()
	dir = t.TempDir()
	objDir = filepath.Join(dir, ".git", "objects")
	if err := os.MkdirAll(filepath.Join(objDir, "pack"), 0o750); err != nil {
		t.Fatal(err)
	}
	return dir, objDir
}

// putLoose writes a loose object file whose zlib stream holds stream
// (header and body as given, so a test can lie in the header).
func putLoose(t *testing.T, objDir, oid string, stream []byte) {
	t.Helper()
	p := filepath.Join(objDir, oid[:2], oid[2:])
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, zlibBytes(stream), 0o600); err != nil {
		t.Fatal(err)
	}
}

// putCommit stores raw as a loose commit under its true id and returns the id.
func putCommit(t *testing.T, objDir string, raw []byte) string {
	t.Helper()
	oid := objID("commit", raw)
	putLoose(t, objDir, oid, append([]byte(fmt.Sprintf("commit %d\x00", len(raw))), raw...))
	return oid
}

// commitRaw builds the content of a commit object.
func commitRaw(author, extraHeaders, msg string, parents ...string) []byte {
	var b bytes.Buffer
	b.WriteString("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n")
	for _, p := range parents {
		b.WriteString("parent " + p + "\n")
	}
	b.WriteString("author " + author + "\n")
	b.WriteString("committer " + author + "\n")
	b.WriteString(extraHeaders)
	b.WriteString("\n" + msg)
	return b.Bytes()
}

const goodAuthor = "Ada Test <ada@example.test> 1700000000 +0100"

// Delta encoding.

func varintLE(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

// dCopy encodes a copy instruction (non-zero bytes only, like git).
func dCopy(off, size int) []byte {
	op := byte(0x80)
	var args []byte
	for k := range 4 {
		if v := byte(off >> (8 * k)); v != 0 {
			op |= 1 << k
			args = append(args, v)
		}
	}
	for k := range 3 {
		if v := byte(size >> (8 * k)); v != 0 {
			op |= 0x10 << k
			args = append(args, v)
		}
	}
	return append([]byte{op}, args...)
}

// dInsert encodes a literal insert of up to 127 bytes.
func dInsert(b []byte) []byte { return append([]byte{byte(len(b))}, b...) }

// mkDelta builds a delta from base size, result size and instructions.
func mkDelta(baseLen, resLen int, ops ...[]byte) []byte {
	d := append(varintLE(baseLen), varintLE(resLen)...)
	for _, o := range ops {
		d = append(d, o...)
	}
	return d
}

// copyAllDelta is a delta that reproduces a base of n bytes unchanged.
func copyAllDelta(n int) []byte { return mkDelta(n, n, dCopy(0, n)) }

// Pack writing.

type packEnt struct {
	typ      int    // 1 commit, 2 tree, 3 blob, 4 tag, 6 ofs delta, 7 ref delta
	data     []byte // content (full entry) or delta bytes
	baseIdx  int    // ofs delta: index of the base entry
	baseSum  []byte // ref delta: base object id (20 bytes)
	oid      string // id the index lists this entry under; empty = not listed
	sizeLie  int    // added to the size in the entry header
	bigIdxOf bool   // list this entry with a 64-bit offset marker in the index
	relForce int    // ofs delta: use this relative offset instead of the real one
}

func packEntHeader(typ, size int) []byte {
	b := byte(typ<<4) | byte(size&15)
	size >>= 4
	var out []byte
	for size > 0 {
		out = append(out, b|0x80)
		b = byte(size & 0x7f)
		size >>= 7
	}
	return append(out, b)
}

func ofsEncode(rel int) []byte {
	buf := []byte{byte(rel & 0x7f)}
	for rel >>= 7; rel != 0; rel >>= 7 {
		rel--
		buf = append([]byte{0x80 | byte(rel&0x7f)}, buf...)
	}
	return buf
}

// writePack writes pack-<name>.pack and .idx (version idxVersion) in objDir/pack.
// It returns the offset of each entry. Trailer checksums are correct, so real
// git reads the result too.
func writePack(t *testing.T, objDir, name string, ents []packEnt, idxVersion uint32) []int64 {
	t.Helper()
	var pack bytes.Buffer
	pack.WriteString("PACK")
	_ = binary.Write(&pack, binary.BigEndian, uint32(2))
	_ = binary.Write(&pack, binary.BigEndian, uint32(len(ents)))
	offs := make([]int64, len(ents))
	crcs := make([]uint32, len(ents))
	for i, e := range ents {
		offs[i] = int64(pack.Len())
		var ent bytes.Buffer
		switch e.typ {
		case 6:
			ent.Write(packEntHeader(6, len(e.data)+e.sizeLie))
			rel := int(offs[i] - offs[e.baseIdx])
			if e.relForce != 0 {
				rel = e.relForce
			}
			ent.Write(ofsEncode(rel))
		case 7:
			ent.Write(packEntHeader(7, len(e.data)+e.sizeLie))
			ent.Write(e.baseSum)
		default:
			ent.Write(packEntHeader(e.typ, len(e.data)+e.sizeLie))
		}
		ent.Write(zlibBytes(e.data))
		crcs[i] = crc32.ChecksumIEEE(ent.Bytes())
		pack.Write(ent.Bytes())
	}
	packSum := sha1.Sum(pack.Bytes()) //nolint:gosec // git trailer
	pack.Write(packSum[:])

	type li struct {
		sum []byte
		i   int
	}
	var list []li
	for i, e := range ents {
		if e.oid == "" {
			continue
		}
		s, _ := hex.DecodeString(e.oid)
		list = append(list, li{s, i})
	}
	sort.Slice(list, func(a, b int) bool { return bytes.Compare(list[a].sum, list[b].sum) < 0 })

	var idx bytes.Buffer
	_ = binary.Write(&idx, binary.BigEndian, uint32(0xff744f63))
	_ = binary.Write(&idx, binary.BigEndian, idxVersion)
	var fan [256]uint32
	for _, l := range list {
		for b := int(l.sum[0]); b < 256; b++ {
			fan[b]++
		}
	}
	for _, v := range fan {
		_ = binary.Write(&idx, binary.BigEndian, v)
	}
	for _, l := range list {
		idx.Write(l.sum)
	}
	for _, l := range list {
		_ = binary.Write(&idx, binary.BigEndian, crcs[l.i])
	}
	for _, l := range list {
		v := uint32(offs[l.i])
		if ents[l.i].bigIdxOf {
			v = 0x80000000
		}
		_ = binary.Write(&idx, binary.BigEndian, v)
	}
	idx.Write(packSum[:])
	idxSum := sha1.Sum(idx.Bytes()) //nolint:gosec // git trailer
	idx.Write(idxSum[:])

	pk := filepath.Join(objDir, "pack")
	if err := os.MkdirAll(pk, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pk, "pack-"+name+".pack"), pack.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pk, "pack-"+name+".idx"), idx.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return offs
}

// openTestPack opens the pack and index written by writePack for readPackEntry.
func openTestPack(t *testing.T, objDir, name string) (*os.File, *packIndex) {
	t.Helper()
	base := filepath.Join(objDir, "pack", "pack-"+name)
	idx, err := openIndex(base + ".idx")
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	pack, err := os.Open(base + ".pack")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.f.Close(); _ = pack.Close() })
	return pack, idx
}

// litDelta is a delta that rebuilds target from literal inserts only, so its
// own size is about the size of target.
func litDelta(baseLen int, target []byte) []byte {
	d := append(varintLE(baseLen), varintLE(len(target))...)
	for len(target) > 0 {
		n := min(len(target), 127)
		d = append(d, dInsert(target[:n])...)
		target = target[n:]
	}
	return d
}
