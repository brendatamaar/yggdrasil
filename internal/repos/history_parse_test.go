package repos

import (
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

var historyTestStart = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
var historyTestOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func validHistoryResult(cs []HistoryCommit, r HistoryRange) bool {
	if len(cs) > 200 || len(cs) > r.Max() {
		return false
	}
	seen := map[string]bool{}
	for i, c := range cs {
		if !historyTestOID.MatchString(c.OID) || seen[c.OID] || c.At.Before(r.From()) || !c.At.Before(r.To()) || c.At.Location() != time.UTC {
			return false
		}
		seen[c.OID] = true
		for _, s := range []string{c.Subject, c.Author} {
			if !utf8.ValidString(s) {
				return false
			}
			for _, ch := range s {
				if unicode.IsControl(ch) {
					return false
				}
			}
		}
		if i > 0 && (cs[i-1].At.Before(c.At) || cs[i-1].At.Equal(c.At) && cs[i-1].OID >= c.OID) {
			return false
		}
	}
	return true
}
func encodeHistory(cs []HistoryCommit) []git.Commit {
	out := make([]git.Commit, len(cs))
	for i, c := range cs {
		out[i] = git.Commit{Hash: c.OID, Subject: c.Subject, Author: c.Author, Date: c.At}
	}
	return out
}

func TestHistoryBoundariesRoundTripAndAcceptedValid(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	for i := 0; i < 300; i++ {
		start := historyTestStart.Add(time.Duration(rng.Intn(10000)) * time.Second).In(time.FixedZone("local", 7*3600))
		end := start.Add(time.Duration(rng.Intn(31*24*3600)+1) * time.Second)
		max := rng.Intn(200) + 1
		r, err := ParseHistoryRange(start, end, max)
		if err != nil {
			t.Fatal(err)
		}
		if r.From().Location() != time.UTC || r.To().Location() != time.UTC || !r.From().Equal(start) || !r.To().Equal(end) || r.Max() != max {
			t.Fatal("range data changed")
		}
		again, err := ParseHistoryRange(r.From(), r.To(), r.Max())
		if err != nil || r != again {
			t.Fatal("range roundtrip", err)
		}
		cs := []git.Commit{{Hash: fmt.Sprintf("%040x", i+1), Date: start, Subject: "change 日本語", Author: "Ada Test"}}
		out, err := parseHistoryCommits(cs, r)
		if err != nil || !validHistoryResult(out, r) {
			t.Fatal("accepted invalid", out, err)
		}
		decoded, err := parseHistoryCommits(encodeHistory(out), r)
		if err != nil || !reflect.DeepEqual(decoded, out) {
			t.Fatal("history roundtrip", err)
		}
	}
	if _, err := ParseHistoryRange(historyTestStart, historyTestStart.Add(31*24*time.Hour), 200); err != nil {
		t.Fatal("exact upper range/count bound rejected", err)
	}
}

func TestHistoryCommitRejectsEachRule(t *testing.T) {
	r, _ := ParseHistoryRange(historyTestStart, historyTestStart.Add(24*time.Hour), 200)
	c := git.Commit{Hash: fmt.Sprintf("%040x", 1), Date: historyTestStart, Subject: "change", Author: "Ada Test"}
	for name, change := range map[string]func(*git.Commit){
		"short OID": func(c *git.Commit) { c.Hash = "abcd" }, "uppercase OID": func(c *git.Commit) { c.Hash = strings.Repeat("A", 40) }, "bad hex": func(c *git.Commit) { c.Hash = "z" + c.Hash[1:] },
		"zero date": func(c *git.Commit) { c.Date = time.Time{} }, "before range": func(c *git.Commit) { c.Date = r.From().Add(-time.Nanosecond) }, "exclusive end": func(c *git.Commit) { c.Date = r.To() },
		"subject control": func(c *git.Commit) { c.Subject = "bad\n" }, "author control": func(c *git.Commit) { c.Author = "bad\u0085" }, "invalid UTF8": func(c *git.Commit) { c.Subject = string([]byte{255}) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			change(&bad)
			if _, err := parseHistoryCommits([]git.Commit{bad}, r); err == nil {
				t.Fatal("invalid commit accepted")
			}
		})
	}
	cases := map[string][]git.Commit{"duplicate": {c, c}, "unordered": {c, {Hash: fmt.Sprintf("%040x", 2), Date: c.Date.Add(time.Hour)}}, "tie order": {{Hash: fmt.Sprintf("%040x", 2), Date: c.Date}, c}, "same OID other date": {{Hash: c.Hash, Date: c.Date.Add(time.Hour)}, c}}
	for name, cs := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseHistoryCommits(cs, r); err == nil {
				t.Fatal("invalid sequence accepted")
			}
		})
	}
	small, _ := ParseHistoryRange(r.From(), r.To(), 1)
	if _, err := parseHistoryCommits([]git.Commit{c, c}, small); err == nil {
		t.Fatal("count limit ignored")
	}
	huge := c
	huge.Subject = strings.Repeat("x", 1<<20)
	if _, err := parseHistoryCommits([]git.Commit{huge}, r); err == nil {
		t.Fatal("byte limit ignored")
	}
}

func FuzzHistoryRange(f *testing.F) {
	f.Add(int64(0), int64(3600), 1)
	f.Add(int64(0), int64(31*24*3600), 200)
	f.Add(int64(0), int64(0), 0)
	f.Fuzz(func(t *testing.T, a, b int64, max int) {
		from := historyTestStart.Add(time.Duration(a%6000000) * time.Second)
		to := historyTestStart.Add(time.Duration(b%6000000) * time.Second)
		r, err := ParseHistoryRange(from, to, max)
		if err == nil {
			if !r.From().Before(r.To()) || r.To().Sub(r.From()) > 31*24*time.Hour || r.Max() < 1 || r.Max() > 200 {
				t.Fatal("invalid range accepted")
			}
			again, err := ParseHistoryRange(r.From(), r.To(), r.Max())
			if err != nil || again != r {
				t.Fatal("roundtrip")
			}
		}
	})
}
func FuzzHistoryCommit(f *testing.F) {
	f.Add(fmt.Sprintf("%040x", 1), "change", "Ada Test", historyTestStart.Unix())
	f.Add("bad", "bad\n", "Ada", int64(0))
	f.Fuzz(func(t *testing.T, oid, subject, author string, at int64) {
		r, _ := ParseHistoryRange(historyTestStart, historyTestStart.Add(24*time.Hour), 200)
		out, err := parseHistoryCommits([]git.Commit{{Hash: oid, Subject: subject, Author: author, Date: time.Unix(at, 0)}}, r)
		if err == nil {
			if !validHistoryResult(out, r) {
				t.Fatal("accepted invalid result")
			}
			again, err := parseHistoryCommits(encodeHistory(out), r)
			if err != nil || !reflect.DeepEqual(again, out) {
				t.Fatal("roundtrip")
			}
		}
	})
}
