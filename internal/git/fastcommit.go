package git

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1" //nolint:gosec // git object ids, not a security use
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Reading one commit straight from the object store saves a git process.
// Starting a process costs 100 to 200 ms on a Windows machine with antivirus,
// and a status refresh of 50 repos would otherwise start 100 of them. The
// read is strictly an optimisation: on anything unexpected the caller runs
// `git log`, which stays the source of truth.

const (
	maxCommitBytes = 1 << 20
	maxDeltaDepth  = 50
	// maxInflateBudget bounds all bytes inflated or produced while resolving
	// one packed commit, so a long chain of large deltas cannot use much memory.
	maxInflateBudget = 8 << 20
	// maxPacks bounds how many pack indexes are tried; beyond it git answers.
	maxPacks = 64
)

var errNoFastPath = errors.New("commit is not readable without git")

// ReadCommitDirect returns the commit with the given 40-hex object id from the
// repo in dir, without starting git. ok is false when it cannot answer for
// sure (SHA-256 repo, linked worktree, alternates, replace refs, a commit with
// an encoding header, a signature it cannot parse, a missing object, a pack
// format it does not know ...); the caller then asks git. Short is the first 7 characters of the id, which
// is what git log shows unless a big repo needs a longer prefix.
// It only reads files under dir/.git.
func ReadCommitDirect(dir, oid string) (c Commit, ok bool) {
	defer func() {
		if recover() != nil { // a damaged object must never crash the server
			c, ok = Commit{}, false
		}
	}()
	return readCommitDirect(dir, oid)
}

// readCommitDirect is ReadCommitDirect without the recover, so tests see panics.
func readCommitDirect(dir, oid string) (c Commit, ok bool) {
	if len(oid) != 40 {
		return Commit{}, false
	}
	if _, err := hex.DecodeString(oid); err != nil {
		return Commit{}, false
	}
	gitDir := filepath.Join(dir, ".git")
	// Real directories only: a symlinked .git or objects could point outside
	// the tracked repo.
	if !isRealDir(gitDir) {
		return Commit{}, false
	}
	obj := filepath.Join(gitDir, "objects")
	if !isRealDir(obj) {
		return Commit{}, false
	}
	// Shallow and grafted repos hide parents; git decides what they mean.
	for _, f := range []string{"shallow", filepath.Join("info", "grafts")} {
		if _, err := os.Lstat(filepath.Join(gitDir, f)); !errors.Is(err, os.ErrNotExist) {
			return Commit{}, false
		}
	}
	if replaceRefsPossible(gitDir) {
		return Commit{}, false
	}
	if _, err := os.Stat(filepath.Join(obj, "info", "alternates")); err == nil {
		return Commit{}, false
	}
	raw, err := readObject(obj, oid)
	if err != nil {
		return Commit{}, false
	}
	// The id is the hash of the content: a wrong or damaged read never passes.
	h := sha1.New() //nolint:gosec // see import
	fmt.Fprintf(h, "commit %d\x00", len(raw))
	h.Write(raw)
	if hex.EncodeToString(h.Sum(nil)) != oid {
		return Commit{}, false
	}
	c, err = parseCommitObject(oid, raw)
	if err != nil {
		return Commit{}, false
	}
	// git log's %h is the shortest unique prefix, at least 7 characters: exactly
	// 7 in a repo with few objects, longer in a big one.
	c.Short = oid[:7]
	return c, true
}

func isRealDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsDir() && fi.Mode()&os.ModeSymlink == 0
}

func replaceRefsPossible(gitDir string) bool {
	if _, err := os.Stat(filepath.Join(gitDir, "refs", "replace")); err == nil {
		return true
	}
	p := filepath.Join(gitDir, "packed-refs")
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil || fi.Size() > 1<<20 {
		return true
	}
	b, err := os.ReadFile(p) //nolint:gosec // a file inside the tracked repo
	return err != nil || bytes.Contains(b, []byte("refs/replace/"))
}

// readObject returns the content of a commit object, loose or packed.
func readObject(objDir, oid string) ([]byte, error) {
	if b, err := readLoose(objDir, oid); err == nil {
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	packs, err := filepath.Glob(filepath.Join(objDir, "pack", "pack-*.idx"))
	if err != nil {
		return nil, err
	}
	sum, _ := hex.DecodeString(oid)
	if len(packs) > maxPacks {
		return nil, errNoFastPath
	}
	for _, idx := range packs {
		b, err := readPacked(idx, sum)
		if err == nil {
			return b, nil
		}
	}
	return nil, errNoFastPath
}

func readLoose(objDir, oid string) ([]byte, error) {
	f, err := os.Open(filepath.Join(objDir, oid[:2], oid[2:])) //nolint:gosec // oid is validated hex; objDir is inside the tracked repo
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, maxCommitBytes+64))
	if err != nil {
		return nil, err
	}
	head, body, found := bytes.Cut(data, []byte{0})
	if !found || !bytes.HasPrefix(head, []byte("commit ")) {
		return nil, errNoFastPath
	}
	n, err := strconv.Atoi(string(head[len("commit "):]))
	if err != nil || n != len(body) || n > maxCommitBytes {
		return nil, errNoFastPath
	}
	return body, nil
}

// packIndex is a version 2 pack index, read with ReadAt.
type packIndex struct {
	f *os.File
	n int64
}

func openIndex(path string) (*packIndex, error) {
	f, err := os.Open(path) //nolint:gosec // a pack index listed from the tracked repo
	if err != nil {
		return nil, err
	}
	var hdr [8 + 1024]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil || binary.BigEndian.Uint32(hdr[0:4]) != 0xff744f63 || binary.BigEndian.Uint32(hdr[4:8]) != 2 {
		_ = f.Close()
		return nil, errNoFastPath
	}
	return &packIndex{f: f, n: int64(binary.BigEndian.Uint32(hdr[8+255*4:]))}, nil
}

// find returns the pack offset of sum, or ok=false.
func (p *packIndex) find(sum []byte) (off int64, ok bool, err error) {
	var fan [4]byte
	lo, hi := int64(0), int64(0)
	if sum[0] > 0 {
		if _, err = p.f.ReadAt(fan[:], 8+int64(sum[0]-1)*4); err != nil {
			return 0, false, err
		}
		lo = int64(binary.BigEndian.Uint32(fan[:]))
	}
	if _, err = p.f.ReadAt(fan[:], 8+int64(sum[0])*4); err != nil {
		return 0, false, err
	}
	hi = int64(binary.BigEndian.Uint32(fan[:]))
	if lo > hi || hi > p.n {
		return 0, false, errNoFastPath // damaged index
	}
	base := int64(8 + 1024)
	var cur [20]byte
	for lo < hi {
		mid := (lo + hi) / 2
		if _, err = p.f.ReadAt(cur[:], base+mid*20); err != nil {
			return 0, false, err
		}
		switch bytes.Compare(cur[:], sum) {
		case 0:
			offTab := base + p.n*20 + p.n*4
			if _, err = p.f.ReadAt(fan[:], offTab+mid*4); err != nil {
				return 0, false, err
			}
			v := binary.BigEndian.Uint32(fan[:])
			if v&0x80000000 != 0 {
				return 0, false, errNoFastPath // 64-bit offsets: ask git
			}
			return int64(v), true, nil
		case -1:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, false, nil
}

func readPacked(idxPath string, sum []byte) ([]byte, error) {
	idx, err := openIndex(idxPath)
	if err != nil {
		return nil, err
	}
	defer idx.f.Close()
	off, ok, err := idx.find(sum)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNoFastPath
	}
	pack, err := os.Open(strings.TrimSuffix(idxPath, ".idx") + ".pack") //nolint:gosec // sibling of a listed pack index
	if err != nil {
		return nil, err
	}
	defer pack.Close()
	budget := int64(maxInflateBudget)
	typ, data, err := readPackEntry(pack, idx, off, 0, &budget)
	if err != nil {
		return nil, err
	}
	if typ != 1 {
		return nil, errNoFastPath
	}
	return data, nil
}

// readPackEntry returns the type (1 commit, 2 tree, 3 blob, 4 tag) and content
// of the entry at off, resolving deltas.
func readPackEntry(pack *os.File, idx *packIndex, off int64, depth int, budget *int64) (int, []byte, error) {
	if depth > maxDeltaDepth {
		return 0, nil, errNoFastPath
	}
	var hdr [32]byte
	n, err := pack.ReadAt(hdr[:], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, nil, err
	}
	if n < 2 {
		return 0, nil, errNoFastPath
	}
	i := 0
	b := hdr[i]
	i++
	typ := int(b>>4) & 7
	size := int64(b & 15)
	shift := uint(4)
	for b&0x80 != 0 {
		if i >= n || shift > 56 {
			return 0, nil, errNoFastPath
		}
		b = hdr[i]
		i++
		size |= int64(b&0x7f) << shift
		shift += 7
	}
	if size > 4*maxCommitBytes {
		return 0, nil, errNoFastPath
	}
	// Each entry inflates `size` bytes and a delta produces about as many; the
	// budget is shared by the whole chain.
	if *budget -= 2 * size; *budget < 0 {
		return 0, nil, errNoFastPath
	}
	dataOff := off + int64(i)
	switch typ {
	case 1, 2, 3, 4:
		data, err := inflateAt(pack, dataOff, size)
		return typ, data, err
	case 6: // OFS_DELTA
		if i >= n {
			return 0, nil, errNoFastPath
		}
		b = hdr[i]
		i++
		rel := int64(b & 0x7f)
		for b&0x80 != 0 {
			if i >= n {
				return 0, nil, errNoFastPath
			}
			b = hdr[i]
			i++
			rel = ((rel + 1) << 7) | int64(b&0x7f)
		}
		if rel <= 0 || rel > off {
			return 0, nil, errNoFastPath
		}
		delta, err := inflateAt(pack, off+int64(i), size)
		if err != nil {
			return 0, nil, err
		}
		bt, base, err := readPackEntry(pack, idx, off-rel, depth+1, budget)
		if err != nil {
			return 0, nil, err
		}
		out, err := applyDelta(base, delta)
		return bt, out, err
	case 7: // REF_DELTA
		if i+20 > n {
			return 0, nil, errNoFastPath
		}
		boff, ok, err := idx.find(hdr[i : i+20])
		if err != nil || !ok {
			return 0, nil, errNoFastPath
		}
		delta, err := inflateAt(pack, off+int64(i)+20, size)
		if err != nil {
			return 0, nil, err
		}
		bt, base, err := readPackEntry(pack, idx, boff, depth+1, budget)
		if err != nil {
			return 0, nil, err
		}
		out, err := applyDelta(base, delta)
		return bt, out, err
	}
	return 0, nil, errNoFastPath
}

func inflateAt(f *os.File, off, size int64) ([]byte, error) {
	zr, err := zlib.NewReader(io.NewSectionReader(f, off, 1<<62))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, errNoFastPath
	}
	return data, nil
}

func deltaVarint(d []byte, i *int) (int, error) {
	v, shift := 0, uint(0)
	for {
		if *i >= len(d) || shift > 28 {
			return 0, errNoFastPath
		}
		b := d[*i]
		*i++
		v |= int(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, nil
		}
		shift += 7
	}
}

func applyDelta(base, delta []byte) ([]byte, error) {
	i := 0
	bs, err := deltaVarint(delta, &i)
	if err != nil || bs != len(base) {
		return nil, errNoFastPath
	}
	rs, err := deltaVarint(delta, &i)
	if err != nil || rs > 4*maxCommitBytes {
		return nil, errNoFastPath
	}
	out := make([]byte, 0, rs)
	for i < len(delta) {
		op := delta[i]
		i++
		if op&0x80 != 0 {
			var off, sz int
			for k := range 4 {
				if op&(1<<k) != 0 {
					if i >= len(delta) {
						return nil, errNoFastPath
					}
					off |= int(delta[i]) << (8 * k)
					i++
				}
			}
			for k := range 3 {
				if op&(0x10<<k) != 0 {
					if i >= len(delta) {
						return nil, errNoFastPath
					}
					sz |= int(delta[i]) << (8 * k)
					i++
				}
			}
			if sz == 0 {
				sz = 0x10000
			}
			if off < 0 || sz < 0 || off+sz > len(base) || len(out)+sz > rs {
				return nil, errNoFastPath
			}
			out = append(out, base[off:off+sz]...)
		} else if op != 0 {
			n := int(op)
			if i+n > len(delta) || len(out)+n > rs {
				return nil, errNoFastPath
			}
			out = append(out, delta[i:i+n]...)
			i += n
		} else {
			return nil, errNoFastPath
		}
	}
	if len(out) != rs {
		return nil, errNoFastPath
	}
	return out, nil
}

// parseCommitObject extracts what `git log --format=%H %an %ae %aI %P %s`
// shows. Anything it is not sure about is an error.
func parseCommitObject(oid string, raw []byte) (Commit, error) {
	head, msg, _ := bytes.Cut(raw, []byte("\n\n"))
	c := Commit{Hash: oid}
	var haveAuthor bool
	for _, line := range strings.Split(string(head), "\n") {
		switch {
		case strings.HasPrefix(line, "parent "):
			c.Parents = append(c.Parents, line[len("parent "):])
		case strings.HasPrefix(line, "author "):
			name, email, when, err := parseIdent(line[len("author "):])
			if err != nil {
				return Commit{}, err
			}
			c.Author, c.Email, c.Date, haveAuthor = name, email, when, true
		case strings.HasPrefix(line, "encoding "):
			return Commit{}, errNoFastPath // git would re-encode to UTF-8
		}
	}
	if !haveAuthor {
		return Commit{}, errNoFastPath
	}
	// Only a plain one-line subject is answered here; git's rules for
	// wrapped or indented subjects are left to git.
	lines := strings.Split(string(msg), "\n")
	first := strings.TrimRight(lines[0], " \t\r")
	switch {
	case len(strings.TrimSpace(string(msg))) == 0:
		first = ""
	case first == "" || first != strings.TrimLeft(first, " \t"):
		return Commit{}, errNoFastPath
	case len(lines) > 1 && strings.TrimSpace(lines[1]) != "":
		return Commit{}, errNoFastPath
	}
	c.Subject = first
	return c, nil
}

// parseIdent reads "Name <email> 1700000000 +0700".
func parseIdent(s string) (name, email string, when time.Time, err error) {
	lt := strings.IndexByte(s, '<')
	gt := strings.LastIndexByte(s, '>')
	if lt < 1 || gt < lt || s[lt-1] != ' ' {
		return "", "", time.Time{}, errNoFastPath
	}
	name, email = s[:lt-1], s[lt+1:gt]
	if strings.ContainsAny(name, "<>") || strings.ContainsAny(email, "<>") {
		return "", "", time.Time{}, errNoFastPath
	}
	f := strings.Fields(s[gt+1:])
	if len(f) != 2 || len(f[1]) != 5 || (f[1][0] != '+' && f[1][0] != '-') {
		return "", "", time.Time{}, errNoFastPath
	}
	ts, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return "", "", time.Time{}, errNoFastPath
	}
	hh, e1 := strconv.Atoi(f[1][1:3])
	mm, e2 := strconv.Atoi(f[1][3:5])
	if e1 != nil || e2 != nil {
		return "", "", time.Time{}, errNoFastPath
	}
	off := hh*3600 + mm*60
	if f[1][0] == '-' {
		off = -off
	}
	// time.Parse(RFC3339) of "+00:00" gives UTC; of other offsets a fixed zone.
	loc := time.UTC
	if off != 0 {
		loc = time.FixedZone("", off)
	}
	return name, email, time.Unix(ts, 0).In(loc), nil
}

// HeadCommit implements the optional direct read of the status reader: the
// commit with object id oid, read from the object store without a process.
// ok is false when git has to answer (see ReadCommitDirect).
func (g *Git) HeadCommit(dir, oid string) (Commit, bool) { return ReadCommitDirect(dir, oid) }
