package git

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Fuzz targets and properties for the direct commit read. They call the inner
// functions, not ReadCommitDirect, so the recover in the public wrapper cannot
// hide a panic: the fuzzer fails on any panic.

var tzShape = regexp.MustCompile(`^[+-][0-9]{4}$`)

// knownBugs switches on the checks that currently fail because of reported
// bugs (see fastcommit_knownbugs_test.go). Run with YGG_KNOWN_BUGS=1 to see them.
var knownBugs = os.Getenv("YGG_KNOWN_BUGS") != ""

// checkAccepted asserts what every accepted commit must satisfy.
func checkAccepted(t *testing.T, oid string, raw []byte, c Commit) {
	t.Helper()
	if len(c.Hash) != 40 {
		t.Errorf("Hash %q is not 40 characters", c.Hash)
	}
	if _, err := hex.DecodeString(c.Hash); err != nil {
		t.Errorf("Hash %q is not hex", c.Hash)
	}
	if c.Hash != oid {
		t.Errorf("Hash %q is not the id asked for %q", c.Hash, oid)
	}
	if c.Date.IsZero() {
		t.Error("accepted commit has a zero date")
	}
	if strings.ContainsAny(c.Subject, "\n") {
		t.Errorf("Subject has a newline: %q", c.Subject)
	}
	if c.Subject != strings.TrimRight(c.Subject, " \t\r") {
		t.Errorf("Subject has trailing whitespace: %q", c.Subject)
	}
	// The author must come from an author header of the object, verbatim.
	head, _, _ := bytes.Cut(raw, []byte("\n\n"))
	found := false
	for _, line := range strings.Split(string(head), "\n") {
		rest, ok := strings.CutPrefix(line, "author ")
		if !ok {
			continue
		}
		if strings.HasPrefix(rest, c.Author+" <"+c.Email+">") {
			found = true
			tz := strings.Fields(rest[strings.LastIndexByte(rest, '>')+1:])
			if knownBugs && len(tz) == 2 && !tzShape.MatchString(tz[1]) {
				t.Errorf("accepted a time zone that is not [+-]hhmm: %q", tz[1])
			}
		}
	}
	if !found {
		t.Errorf("author %q <%q> does not appear in any author header of %q", c.Author, c.Email, head)
	}
}

func FuzzParseCommitObject(f *testing.F) {
	oid := strings.Repeat("0123456789", 4)
	f.Add(commitRaw(goodAuthor, "", "hello\n"))
	f.Add(commitRaw(goodAuthor, "", ""))
	f.Add(commitRaw("Ada <a@b> 0 -0000", "", "subject  \t\n\nbody\n", strings.Repeat("a", 40), strings.Repeat("b", 40)))
	f.Add(commitRaw(goodAuthor, "gpgsig -----BEGIN-----\n line\n -----END-----\n", "signed\n"))
	f.Add(commitRaw(goodAuthor, "", "crlf subject\r\n\r\nbody\r\n"))
	f.Add([]byte("author  <> 1 +0000\n\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		c, err := parseCommitObject(oid, raw)
		if err != nil {
			return
		}
		checkAccepted(t, oid, raw, c)
	})
}

func FuzzParseIdent(f *testing.F) {
	f.Add(goodAuthor)
	f.Add("A <> 0 -0000")
	f.Add("Zoë Ünï <z@example.test> -5 +0545")
	f.Add("x <y> 1 +9999")
	f.Fuzz(func(t *testing.T, s string) {
		name, email, when, err := parseIdent(s)
		if err != nil {
			return
		}
		if strings.ContainsAny(name, "<>") || strings.ContainsAny(email, "<>") {
			t.Errorf("accepted <> inside name or email: %q %q", name, email)
		}
		if !strings.HasPrefix(s, name+" <"+email+">") {
			t.Errorf("%q does not start with the parsed %q <%q>", s, name, email)
		}
		fields := strings.Fields(s[strings.LastIndexByte(s, '>')+1:])
		if len(fields) != 2 {
			t.Fatalf("accepted %q with %d trailing fields", s, len(fields))
		}
		if knownBugs && !tzShape.MatchString(fields[1]) {
			t.Errorf("accepted time zone %q", fields[1])
		}
		ts, perr := strconv.ParseInt(fields[0], 10, 64)
		if perr != nil || when.Unix() != ts {
			t.Errorf("timestamp %q parsed as %d (err %v)", fields[0], when.Unix(), perr)
		}
		_, off := when.Zone()
		if !tzShape.MatchString(fields[1]) {
			return
		}
		hh, _ := strconv.Atoi(fields[1][1:3])
		mm, _ := strconv.Atoi(fields[1][3:5])
		want := hh*3600 + mm*60
		if fields[1][0] == '-' {
			want = -want
		}
		if off != want {
			t.Errorf("zone offset %d, want %d for %q", off, want, fields[1])
		}
	})
}

// formatIdent is the inverse of parseIdent.
func formatIdent(name, email string, ts int64, offMin int) string {
	sign := '+'
	if offMin < 0 {
		sign, offMin = '-', -offMin
	}
	return fmt.Sprintf("%s <%s> %d %c%02d%02d", name, email, ts, sign, offMin/60, offMin%60)
}

func FuzzParseIdentRoundTrip(f *testing.F) {
	f.Add("Ada Test", "ada@example.test", int64(1700000000), int16(60))
	f.Add("", "", int64(0), int16(0))
	f.Add("Zoë", "z@x", int64(-1), int16(-345))
	f.Fuzz(func(t *testing.T, name, email string, ts int64, off int16) {
		if strings.ContainsAny(name+email, "<>\n\r\x00") || !utf8.ValidString(name+email) ||
			strings.TrimSpace(strings.TrimRight(name, " ")) == "" && name != "" {
			t.Skip()
		}
		if ts < -1<<40 || ts > 1<<40 {
			t.Skip() // beyond what any commit holds; time.Time arithmetic stays exact inside
		}
		offMin := int(off) % (24 * 60)
		line := formatIdent(name, email, ts, offMin)
		gn, ge, when, err := parseIdent(line)
		if err != nil {
			t.Fatalf("parseIdent(format(x)) failed for %q: %v", line, err)
		}
		_, zoff := when.Zone()
		if gn != name || ge != email || when.Unix() != ts || zoff != offMin*60 {
			t.Errorf("round trip of %q: got %q <%q> %d %d", line, gn, ge, when.Unix(), zoff)
		}
	})
}

func FuzzCommitObjectRoundTrip(f *testing.F) {
	f.Add("Ada Test", "ada@example.test", "fix the cart total", int64(1700000000), int16(-90))
	f.Add("A", "a@b", "", int64(5), int16(0))
	f.Fuzz(func(t *testing.T, name, email, subject string, ts int64, off int16) {
		if strings.ContainsAny(name+email, "<>\n\r\x00") || strings.ContainsAny(subject, "\n\x00") ||
			strings.TrimSpace(subject) != subject || ts < 0 || ts > 1<<40 || name == "" ||
			strings.TrimSpace(name) != name || !utf8.ValidString(name+email+subject) {
			t.Skip()
		}
		raw := commitRaw(formatIdent(name, email, ts, int(off)%(24*60)), "", subject+"\n")
		oid := objID("commit", raw)
		c, err := parseCommitObject(oid, raw)
		if err != nil {
			t.Fatalf("a well-formed commit was rejected: %v\n%q", err, raw)
		}
		checkAccepted(t, oid, raw, c)
		if c.Author != name || c.Email != email || c.Subject != subject || c.Date.Unix() != ts {
			t.Errorf("round trip: got %+v", c)
		}
	})
}

// refApplyDelta is a plain reading of git's delta format, used as the oracle.
func refApplyDelta(base, delta []byte) ([]byte, bool) {
	pos := 0
	varint := func() (int, bool) {
		v, shift := 0, 0
		for {
			if pos >= len(delta) || shift > 28 {
				return 0, false
			}
			b := delta[pos]
			pos++
			v |= int(b&0x7f) << shift
			shift += 7
			if b&0x80 == 0 {
				return v, true
			}
		}
	}
	bs, ok := varint()
	if !ok || bs != len(base) {
		return nil, false
	}
	rs, ok := varint()
	if !ok {
		return nil, false
	}
	var out []byte
	for pos < len(delta) {
		op := delta[pos]
		pos++
		switch {
		case op&0x80 != 0:
			off, sz := 0, 0
			for k := range 4 {
				if op&(1<<k) != 0 {
					if pos >= len(delta) {
						return nil, false
					}
					off |= int(delta[pos]) << (8 * k)
					pos++
				}
			}
			for k := range 3 {
				if op&(0x10<<k) != 0 {
					if pos >= len(delta) {
						return nil, false
					}
					sz |= int(delta[pos]) << (8 * k)
					pos++
				}
			}
			if sz == 0 {
				sz = 0x10000
			}
			if off+sz > len(base) {
				return nil, false
			}
			out = append(out, base[off:off+sz]...)
		case op != 0:
			if pos+int(op) > len(delta) {
				return nil, false
			}
			out = append(out, delta[pos:pos+int(op)]...)
			pos += int(op)
		default:
			return nil, false
		}
	}
	return out, len(out) == rs
}

func FuzzApplyDelta(f *testing.F) {
	base := []byte("0123456789abcdef")
	f.Add(base, copyAllDelta(len(base)))
	f.Add(base, mkDelta(16, 20, dCopy(4, 8), dInsert([]byte("tail")), dCopy(0, 8)))
	f.Add(base, mkDelta(16, 3, dInsert([]byte("xyz"))))
	f.Add(base, []byte{16, 0x80, 0x80, 0x04, 0x80}) // copy of 0x10000 from a 16 byte base
	f.Add([]byte{}, []byte{0, 0})
	f.Fuzz(func(t *testing.T, base, delta []byte) {
		out, err := applyDelta(base, delta)
		if err != nil {
			return
		}
		i := 0
		bs, err1 := deltaVarint(delta, &i)
		rs, err2 := deltaVarint(delta, &i)
		if err1 != nil || err2 != nil {
			t.Fatalf("accepted a delta whose header does not parse")
		}
		if bs != len(base) {
			t.Errorf("accepted base size %d for a base of %d", bs, len(base))
		}
		if len(out) != rs {
			t.Errorf("output %d bytes, declared %d", len(out), rs)
		}
		if len(out) > 4*maxCommitBytes {
			t.Errorf("output %d bytes is above the limit", len(out))
		}
		want, ok := refApplyDelta(base, delta)
		if !ok || !bytes.Equal(want, out) {
			t.Errorf("accepted a delta the reference rejects or reads differently (ref ok=%v)", ok)
		}
	})
}

// entryBytes is one full pack entry (header and zlib stream).
func entryBytes(typ int, data []byte) []byte {
	return append(packEntHeader(typ, len(data)), zlibBytes(data)...)
}

func FuzzReadPackEntry(f *testing.F) {
	raw := commitRaw(goodAuthor, "", "fuzz subject\n")
	e0 := entryBytes(1, raw)
	f.Add(e0, uint16(0))
	d := copyAllDelta(len(raw))
	e1 := append(append(packEntHeader(6, len(d)), ofsEncode(len(e0))...), zlibBytes(d)...)
	f.Add(append(append([]byte(nil), e0...), e1...), uint16(len(e0)))
	e2 := append(append(packEntHeader(7, len(d)), make([]byte, 20)...), zlibBytes(d)...)
	f.Add(e2, uint16(0))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}, uint16(0))
	f.Add([]byte{0x00}, uint16(0))
	f.Fuzz(func(t *testing.T, data []byte, off uint16) {
		_, obj := fakeRepo(t)
		writePack(t, obj, "fz", nil, 2)
		packPath := filepath.Join(obj, "pack", "pack-fz.pack")
		if err := os.WriteFile(packPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		pack, idx := openTestPack(t, obj, "fz")
		budget := int64(maxInflateBudget)
		o := int64(0)
		if len(data) > 0 {
			o = int64(int(off) % len(data))
		}
		typ, out, err := readPackEntry(pack, idx, o, 0, &budget)
		if err != nil {
			return
		}
		if typ < 1 || typ > 4 {
			t.Errorf("accepted entry type %d", typ)
		}
		if budget < 0 {
			t.Errorf("budget went negative (%d) on success", budget)
		}
		if len(out) > 4*maxCommitBytes {
			t.Errorf("entry of %d bytes is above the size limit", len(out))
		}
	})
}

// A fuzz seed that exercises the whole path: any pack entry bytes in a pack
// next to a real index must either give a commit that hashes to the id or no
// answer.
func FuzzReadCommitDirectPack(f *testing.F) {
	raw := commitRaw(goodAuthor, "", "fuzz subject\n")
	f.Add(raw, []byte{0})
	f.Add(raw, copyAllDelta(len(raw)))
	f.Fuzz(func(t *testing.T, content, delta []byte) {
		dir, obj := fakeRepo(t)
		oid := objID("commit", content)
		ents := []packEnt{{typ: 1, data: content}, {typ: 6, baseIdx: 0, data: delta, oid: oid}}
		writePack(t, obj, "fz", ents, 2)
		c, ok := readCommitDirect(dir, oid)
		if !ok {
			return
		}
		checkAccepted(t, oid, content, c)
	})
}
