package repos

// Differential and property tests of ParseHistoryRange and parseHistoryCommits
// (AU-02). The oracles below are written from the design (docs/design/
// phase-6-standup.md and git.LogRange's documented contract): they share no code
// with historyOID, historyText or the parsers under test.

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

const goZeroUnix = -62135596800 // 0001-01-01T00:00:00Z

func isZeroInstant(t time.Time) bool { return t.Unix() == goZeroUnix && t.Nanosecond() == 0 }

// oracleRangeOK states which ranges are valid: both instants nonzero, strictly
// increasing, at most 31 days apart (exactly 31 days is allowed), count in 1..200.
func oracleRangeOK(from, to time.Time, max int) bool {
	if isZeroInstant(from) || isZeroInstant(to) || max < 1 || max > 200 {
		return false
	}
	sec := to.Unix() - from.Unix()
	ns := int64(to.Nanosecond() - from.Nanosecond())
	if sec > 1_000_000_000 || sec < -1_000_000_000 { // far beyond 31 days either way
		return false
	}
	total := sec*1_000_000_000 + ns
	return total > 0 && total <= 31*24*3600*1_000_000_000
}

func oracleText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.Is(unicode.Cc, r) {
			return false
		}
	}
	return true
}

// oracleCommitsOK states which result lists are valid for a range.
func oracleCommitsOK(cs []git.Commit, from, to time.Time, max int) bool {
	if len(cs) > max {
		return false
	}
	total := 0
	seen := map[string]bool{}
	for i, c := range cs {
		total += len(c.Hash) + len(c.Subject) + len(c.Author)
		if total > 1<<20 || !historyTestOID.MatchString(c.Hash) || !oracleText(c.Subject) || !oracleText(c.Author) {
			return false
		}
		if isZeroInstant(c.Date) || c.Date.Before(from) || !c.Date.Before(to) || seen[c.Hash] {
			return false
		}
		seen[c.Hash] = true
		if i > 0 {
			prev := cs[i-1]
			if prev.Date.Before(c.Date) || (prev.Date.Equal(c.Date) && prev.Hash >= c.Hash) {
				return false
			}
		}
	}
	return true
}

func TestPropParseHistoryRangeMatchesOracle(t *testing.T) {
	bases := []time.Time{
		{}, time.Unix(goZeroUnix, 1), time.Unix(0, 0), time.Unix(0, 1), historyTestStart,
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), time.Unix(1<<62, 0), time.Unix(-1<<62, 0),
		time.Date(1, 1, 1, 0, 0, 1, 0, time.UTC), time.Date(2262, 4, 11, 23, 47, 16, 854775807, time.UTC), time.Date(1677, 9, 21, 0, 12, 43, 0, time.UTC),
	}
	deltas := []time.Duration{0, 1, -1, time.Nanosecond * 999, time.Second, -time.Second, time.Hour, 24 * time.Hour, 31*24*time.Hour - 1, 31 * 24 * time.Hour, 31*24*time.Hour + 1, -31 * 24 * time.Hour, 1<<63 - 1, -1 << 63}
	zones := []*time.Location{time.UTC, time.FixedZone("p7", 7*3600), time.FixedZone("m5", -5*3600), time.FixedZone("p930", 9*3600+1800)}
	counts := []int{-1, 0, 1, 2, 199, 200, 201, 1 << 30}
	accepted, rejected := 0, 0
	for _, a := range bases {
		for _, d := range deltas {
			for _, off := range []time.Duration{0, 1, 31 * 24 * time.Hour} {
				from := a.Add(off)
				to := from.Add(d)
				if d == 1<<63-1 || d == -1<<63 { // saturating Add: independent far-apart instants
					to = a.Add(d)
				}
				for _, mx := range counts {
					zf, zt := zones[(int(off)+int(d))&3], zones[(int(d)>>2)&3]
					f, tt := from.In(zf), to.In(zt)
					r, err := ParseHistoryRange(f, tt, mx)
					want := oracleRangeOK(f, tt, mx)
					if (err == nil) != want {
						t.Fatalf("from=%v to=%v max=%d: parser err=%v, oracle accepts=%v", f, tt, mx, err, want)
					}
					if err != nil {
						rejected++
						continue
					}
					accepted++
					if !r.From().Equal(f) || !r.To().Equal(tt) || r.From().Nanosecond() != f.Nanosecond() || r.To().Nanosecond() != tt.Nanosecond() || r.Max() != mx || r.From().Location() != time.UTC || r.To().Location() != time.UTC {
						t.Fatalf("accepted range changed an instant: %v %v -> %v %v", f, tt, r.From(), r.To())
					}
					again, err := ParseHistoryRange(r.From(), r.To(), r.Max())
					if err != nil || again != r {
						t.Fatalf("round trip: %v %v", again, err)
					}
				}
			}
		}
	}
	if accepted < 200 || rejected < 1000 {
		t.Fatalf("generator is lopsided: accepted=%d rejected=%d", accepted, rejected)
	}
}

// The exact edges, spelled out: one nanosecond makes the difference.
func TestParseHistoryRangeNanosecondEdges(t *testing.T) {
	from := historyTestStart.Add(123456789 * time.Nanosecond)
	ok := func(to time.Time, max int) bool { _, err := ParseHistoryRange(from, to, max); return err == nil }
	for name, tc := range map[string]struct {
		to   time.Time
		max  int
		want bool
	}{
		"1ns wide":              {from.Add(1), 1, true},
		"empty":                 {from, 1, false},
		"reversed by 1ns":       {from.Add(-1), 1, false},
		"31 days exactly":       {from.Add(31 * 24 * time.Hour), 200, true},
		"31 days and 1ns":       {from.Add(31*24*time.Hour + 1), 200, false},
		"count 1":               {from.Add(time.Hour), 1, true},
		"count 200":             {from.Add(time.Hour), 200, true},
		"count 0":               {from.Add(time.Hour), 0, false},
		"count 201":             {from.Add(time.Hour), 201, false},
		"count negative":        {from.Add(time.Hour), -1, false},
		"zero end":              {time.Time{}, 1, false},
		"year 9999 end":         {time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), 1, false},
		"far future (overflow)": {time.Unix(1<<62, 0), 1, false},
	} {
		if got := ok(tc.to, tc.max); got != tc.want {
			t.Errorf("%s: accepted=%v, want %v", name, got, tc.want)
		}
	}
	if _, err := ParseHistoryRange(time.Time{}, historyTestStart, 1); err == nil {
		t.Error("zero start accepted")
	}
	// The zero instant is zero in any zone.
	if _, err := ParseHistoryRange(time.Time{}.In(time.FixedZone("p7", 7*3600)), historyTestStart, 1); err == nil {
		t.Error("zero start in another zone accepted")
	}
	if _, err := ParseHistoryRange(time.Unix(-1<<62, 0), historyTestStart, 1); err == nil {
		t.Error("far past start accepted")
	}
	// The monotonic clock reading never leaks into the range value.
	now := time.Now()
	r, err := ParseHistoryRange(now, now.Add(time.Hour), 5)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := ParseHistoryRange(r.From(), r.To(), 5); again != r {
		t.Error("range holds a monotonic reading")
	}
	// The exact end instant is retained to the nanosecond.
	end := from.Add(time.Hour + 7)
	r, _ = ParseHistoryRange(from, end, 5)
	if r.To().Sub(r.From()) != time.Hour+7 {
		t.Errorf("span %v", r.To().Sub(r.From()))
	}
}

func FuzzHistoryRangeOracle(f *testing.F) {
	f.Add(int64(0), int64(0), int64(3600), int64(0), 1)
	f.Add(int64(goZeroUnix), int64(0), int64(goZeroUnix+10), int64(0), 5)
	f.Add(int64(1), int64(5), int64(1+31*24*3600), int64(5), 200)
	f.Add(int64(1), int64(5), int64(1+31*24*3600), int64(6), 200)
	f.Add(int64(1<<62), int64(0), int64(-1<<62), int64(0), 1)
	f.Fuzz(func(t *testing.T, s1, n1, s2, n2 int64, max int) {
		from, to := time.Unix(s1, n1%2_000_000_000), time.Unix(s2, n2%2_000_000_000)
		r, err := ParseHistoryRange(from, to, max)
		if want := oracleRangeOK(from, to, max); (err == nil) != want {
			t.Fatalf("from=%v to=%v max=%d: err=%v, oracle %v", from, to, max, err, want)
		}
		if err == nil {
			if again, err := ParseHistoryRange(r.From(), r.To(), r.Max()); err != nil || again != r {
				t.Fatal("round trip")
			}
		}
	})
}

// ---- parseHistoryCommits -------------------------------------------------

var histPoolSubjects = []string{"fix cart total", "日本語 の件名", "ünï: c0de", "release 1.2.0", "a b  c", ""}

func histOID(r *rand.Rand, wide bool) string {
	n := 40
	if wide {
		n = 64
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte("0123456789abcdef"[r.Intn(16)])
	}
	return b.String()
}

// validHistory builds a representable result: unique hashes of either width,
// author times inside the range, newest first, equal times by ascending hash.
func validHistory(r *rand.Rand, from, to time.Time, n int) []git.Commit {
	var cs []git.Commit
	seen := map[string]bool{}
	span := int64(to.Sub(from) / time.Second)
	for len(cs) < n {
		h := histOID(r, r.Intn(4) == 0)
		if seen[h] {
			continue
		}
		seen[h] = true
		at := from.Add(time.Duration(r.Int63n(max(span, 1))) * time.Second)
		if r.Intn(3) == 0 && len(cs) > 0 {
			at = cs[r.Intn(len(cs))].Date // ties
		}
		if r.Intn(8) == 0 {
			at = at.Add(time.Duration(r.Intn(1000)) * time.Nanosecond).Truncate(time.Nanosecond)
			if !at.Before(to) {
				at = from
			}
		}
		cs = append(cs, git.Commit{Hash: h, Subject: histPoolSubjects[r.Intn(len(histPoolSubjects))], Author: "Ada Test", Date: at.In(time.FixedZone("z", 3600*(r.Intn(15)-7)))})
	}
	slices.SortFunc(cs, func(a, b git.Commit) int {
		if !a.Date.Equal(b.Date) {
			if a.Date.After(b.Date) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Hash, b.Hash)
	})
	return cs
}

// mutateHistory applies one fault, or none.
func mutateHistory(r *rand.Rand, cs []git.Commit, from, to time.Time) []git.Commit {
	cs = slices.Clone(cs)
	if len(cs) == 0 {
		return cs
	}
	i := r.Intn(len(cs))
	switch r.Intn(20) {
	case 0:
		cs[i].Hash = strings.ToUpper(cs[i].Hash)
	case 1:
		cs[i].Hash = cs[i].Hash[:len(cs[i].Hash)-1]
	case 2:
		cs[i].Hash += "0"
	case 3:
		cs[i].Hash = "g" + cs[i].Hash[1:]
	case 4:
		cs[i].Subject = "bad\nsubject"
	case 5:
		cs[i].Author = "bad\x00author"
	case 6:
		cs[i].Subject = "bad\xffutf8"
	case 7:
		cs[i].Author = "esc\x1b[0m"
	case 8:
		cs[i].Date = time.Time{}
	case 9:
		cs[i].Date = from.Add(-time.Nanosecond)
	case 10:
		cs[i].Date = to
	case 11:
		cs[i].Date = to.Add(time.Hour)
	case 12:
		cs = append(cs, cs[i]) // duplicate
	case 13:
		if len(cs) > 1 {
			j := r.Intn(len(cs))
			cs[i], cs[j] = cs[j], cs[i]
		}
	case 14:
		cs[i].Date = from // exact start: still valid, but may break the order
	case 15:
		cs[i].Date = to.Add(-time.Nanosecond)
	case 16:
		cs[i].Subject = strings.Repeat("é", 300000)
	default: // no fault
	}
	return cs
}

func TestPropParseHistoryCommitsMatchesOracle(t *testing.T) {
	accepted, rejected := 0, 0
	for seed := int64(1); seed <= 1500; seed++ {
		r := rand.New(rand.NewSource(seed))
		from := historyTestStart.Add(time.Duration(r.Intn(100000)) * time.Second)
		to := from.Add(time.Duration(1+r.Intn(30*24*3600)) * time.Second)
		maxN := []int{1, 2, 5, 200}[r.Intn(4)]
		rng, err := ParseHistoryRange(from, to, maxN)
		if err != nil {
			t.Fatal(err)
		}
		n := r.Intn(min(maxN, 6) + 1)
		if r.Intn(12) == 0 {
			n = maxN + 1 // more than was asked for
		}
		cs := mutateHistory(r, validHistory(r, from, to, min(n, 12)), from, to)
		out, err := parseHistoryCommits(cs, rng)
		want := oracleCommitsOK(cs, from, to, maxN)
		if (err == nil) != want {
			t.Fatalf("seed %d: parser err=%v, oracle accepts=%v\nrange [%v, %v) max %d\ninput %+v", seed, err, want, from, to, maxN, abbreviate(cs))
		}
		if err != nil {
			rejected++
			if out != nil {
				t.Fatalf("seed %d: a refusal returned data", seed)
			}
			continue
		}
		accepted++
		if !validHistoryResult(out, rng) {
			t.Fatalf("seed %d: accepted result is invalid", seed)
		}
		for i, c := range cs {
			if out[i].OID != c.Hash || out[i].Subject != c.Subject || out[i].Author != c.Author || !out[i].At.Equal(c.Date) || out[i].At.Location() != time.UTC {
				t.Fatalf("seed %d: record %d changed: %+v from %+v", seed, i, out[i], c)
			}
		}
		again, err := parseHistoryCommits(encodeHistory(out), rng)
		if err != nil || !reflect.DeepEqual(again, out) {
			t.Fatalf("seed %d: round trip failed: %v", seed, err)
		}
	}
	if accepted < 500 || rejected < 300 {
		t.Fatalf("generator is lopsided: accepted=%d rejected=%d", accepted, rejected)
	}
}

func abbreviate(cs []git.Commit) []string {
	var out []string
	for _, c := range cs {
		s := c.Subject
		if len(s) > 12 {
			s = s[:12] + "…"
		}
		h := c.Hash
		if len(h) > 6 {
			h = h[:6]
		}
		out = append(out, fmt.Sprintf("%s|%q|%q|%s", h, s, c.Author, c.Date.UTC().Format(time.RFC3339Nano)))
	}
	return out
}

// The byte limit counts hash, subject and author bytes of the whole list, and
// the boundary is exact.
func TestParseHistoryCommitsByteLimitIsExact(t *testing.T) {
	from := historyTestStart
	to := from.Add(24 * time.Hour)
	rng, _ := ParseHistoryRange(from, to, 200)
	mk := func(i int, subject string) git.Commit {
		return git.Commit{Hash: fmt.Sprintf("%040x", i), Subject: subject, Author: "Ada", Date: to.Add(-time.Duration(i) * time.Hour)}
	}
	fixed := 40 + len("Ada")
	for name, tc := range map[string]struct {
		cs   []git.Commit
		want bool
	}{
		"one record, exactly 1 MiB":      {[]git.Commit{mk(1, strings.Repeat("x", 1<<20-fixed))}, true},
		"one record, 1 MiB + 1":          {[]git.Commit{mk(1, strings.Repeat("x", 1<<20-fixed+1))}, false},
		"multibyte, exactly 1 MiB":       {[]git.Commit{mk(1, strings.Repeat("é", (1<<20-fixed)/2)+strings.Repeat("x", (1<<20-fixed)%2))}, true},
		"multibyte, 1 MiB + 1":           {[]git.Commit{mk(1, strings.Repeat("é", (1<<20-fixed)/2)+strings.Repeat("x", (1<<20-fixed)%2+1))}, false},
		"two records, exactly 1 MiB":     {[]git.Commit{mk(1, strings.Repeat("x", 1<<19-fixed)), mk(2, strings.Repeat("x", 1<<19-fixed))}, true},
		"two records, 1 MiB + 1":         {[]git.Commit{mk(1, strings.Repeat("x", 1<<19-fixed)), mk(2, strings.Repeat("x", 1<<19-fixed+1))}, false},
		"small records, count limit 200": {manyCommits(200, to), true},
	} {
		_, err := parseHistoryCommits(tc.cs, rng)
		if (err == nil) != tc.want {
			t.Errorf("%s: accepted=%v, want %v", name, err == nil, tc.want)
		}
		if want := oracleCommitsOK(tc.cs, from, to, 200); want != tc.want {
			t.Errorf("%s: the oracle disagrees with the table (%v)", name, want)
		}
	}
	if _, err := parseHistoryCommits(manyCommits(201, to), rng); err == nil {
		t.Error("201 records accepted for max 200")
	}
}

func manyCommits(n int, to time.Time) []git.Commit {
	var out []git.Commit
	for i := 0; i < n; i++ {
		out = append(out, git.Commit{Hash: fmt.Sprintf("%040x", i+1), Subject: "s", Author: "a", Date: to.Add(-time.Duration(i+1) * time.Second)})
	}
	return out
}

// Nanosecond edges of the interval, and equal instants written in different
// zones, are decided by the instant, not by the zone or by rounding.
func TestParseHistoryCommitsInstantEdges(t *testing.T) {
	from := historyTestStart.Add(500 * time.Nanosecond)
	to := from.Add(time.Hour)
	rng, _ := ParseHistoryRange(from, to, 10)
	oid := func(i int) string { return fmt.Sprintf("%040x", i) }
	p7 := time.FixedZone("p7", 7*3600)
	for name, tc := range map[string]struct {
		cs   []git.Commit
		want bool
	}{
		"exactly the start":           {[]git.Commit{{Hash: oid(1), Date: from}}, true},
		"1ns before the start":        {[]git.Commit{{Hash: oid(1), Date: from.Add(-1)}}, false},
		"1ns before the end":          {[]git.Commit{{Hash: oid(1), Date: to.Add(-1)}}, true},
		"exactly the end":             {[]git.Commit{{Hash: oid(1), Date: to}}, false},
		"start written in +07:00":     {[]git.Commit{{Hash: oid(1), Date: from.In(p7)}}, true},
		"end written in +07:00":       {[]git.Commit{{Hash: oid(1), Date: to.In(p7)}}, false},
		"same instant, two zones tie": {[]git.Commit{{Hash: oid(1), Date: from.Add(time.Minute)}, {Hash: oid(2), Date: from.Add(time.Minute).In(p7)}}, true},
		"same instant, hash reversed": {[]git.Commit{{Hash: oid(2), Date: from.Add(time.Minute)}, {Hash: oid(1), Date: from.Add(time.Minute).In(p7)}}, false},
		"1ns apart, newest first":     {[]git.Commit{{Hash: oid(1), Date: from.Add(time.Minute + 1)}, {Hash: oid(2), Date: from.Add(time.Minute)}}, true},
		"1ns apart, oldest first":     {[]git.Commit{{Hash: oid(1), Date: from.Add(time.Minute)}, {Hash: oid(2), Date: from.Add(time.Minute + 1)}}, false},
	} {
		for i := range tc.cs {
			tc.cs[i].Subject, tc.cs[i].Author = "s", "a"
		}
		out, err := parseHistoryCommits(tc.cs, rng)
		if (err == nil) != tc.want {
			t.Errorf("%s: accepted=%v, want %v", name, err == nil, tc.want)
		}
		if err == nil {
			for i, c := range out {
				if !c.At.Equal(tc.cs[i].Date) || c.At.Nanosecond() != tc.cs[i].Date.Nanosecond() || c.At.Location() != time.UTC {
					t.Errorf("%s: instant changed: %v from %v", name, c.At, tc.cs[i].Date)
				}
			}
		}
	}
}

// Multi-record fuzzing: one line per record, tab separated: oid, subject,
// author, seconds from the range start, nanoseconds. Whatever the fuzzer builds,
// the parser agrees with the oracle on the whole list.
func FuzzParseHistoryCommitsMultiRecord(f *testing.F) {
	line := func(oid, subject, author string, sec, ns int64) string {
		return strings.Join([]string{oid, subject, author, strconv.FormatInt(sec, 10), strconv.FormatInt(ns, 10)}, "\t")
	}
	a, b, c := fmt.Sprintf("%040x", 3), fmt.Sprintf("%040x", 2), strings.Repeat("ab", 32)
	f.Add(strings.Join([]string{line(a, "newest", "Ada Test", 7200, 0), line(b, "tie", "Ada Test", 3600, 0), line(c, "tie later hash", "Ada Test", 3600, 0)}, "\n"))
	f.Add(strings.Join([]string{line(a, "x", "y", 10, 0), line(a, "dup", "y", 5, 0)}, "\n"))
	f.Add(strings.Join([]string{line(a, "old first", "y", 1, 0), line(b, "new second", "y", 9, 0)}, "\n"))
	f.Add(strings.Join([]string{line(a, "bad\u0085", "y", 1, 0)}, "\n"))
	f.Add(strings.Join([]string{line(a, "ok", "y", 86400, 0)}, "\n"))
	f.Add(strings.Join([]string{line(a, "ok", "y", 86399, 999999999), line(b, "ok", "y", 0, 0)}, "\n"))
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		from := historyTestStart
		to := from.Add(24 * time.Hour)
		rng, _ := ParseHistoryRange(from, to, 200)
		var cs []git.Commit
		if raw != "" {
			for _, ln := range strings.Split(raw, "\n") {
				p := strings.Split(ln, "\t")
				c := git.Commit{}
				if len(p) > 0 {
					c.Hash = p[0]
				}
				if len(p) > 1 {
					c.Subject = p[1]
				}
				if len(p) > 2 {
					c.Author = p[2]
				}
				if len(p) > 4 {
					sec, e1 := strconv.ParseInt(p[3], 10, 64)
					ns, e2 := strconv.ParseInt(p[4], 10, 64)
					if e1 == nil && e2 == nil {
						c.Date = time.Unix(from.Unix()+sec, ns)
					}
				}
				cs = append(cs, c)
			}
		}
		out, err := parseHistoryCommits(cs, rng)
		if want := oracleCommitsOK(cs, from, to, 200); (err == nil) != want {
			t.Fatalf("parser err=%v, oracle accepts=%v for %q", err, want, raw)
		}
		if err == nil {
			if !validHistoryResult(out, rng) {
				t.Fatal("accepted an invalid result")
			}
			if again, err := parseHistoryCommits(encodeHistory(out), rng); err != nil || !reflect.DeepEqual(again, out) {
				t.Fatal("round trip")
			}
		}
	})
}
