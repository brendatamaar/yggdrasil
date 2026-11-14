package git

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func rangeTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

// Dates deliberately disagree, and ancestry order is not author-date order.
func TestLogRangeContract(t *testing.T) {
	f := newFx(t)
	dir := f.repo("history")
	from := rangeTime("2025-03-02T12:00:00Z")
	to := from.Add(2 * time.Second)
	commits := []struct{ a, c string }{
		{"2025-03-02T12:00:00Z", "2025-06-01T00:00:00Z"},
		{"2025-03-02T12:00:02Z", "2025-03-02T12:00:00Z"},
		{"2025-03-02T12:00:01Z", "2025-01-01T00:00:00Z"},
		{"2025-03-02T19:00:01+07:00", "2025-01-02T00:00:00Z"},
	}
	var expected []Commit
	for i, d := range commits {
		old := f.env
		f.env = append(append([]string{}, old...), "GIT_AUTHOR_DATE="+d.a, "GIT_COMMITTER_DATE="+d.c)
		f.git(dir, "commit", "--allow-empty", "-q", "-m", fmt.Sprintf("event %d", i))
		f.env = old
		log, err := f.g.Log(bg(), dir, 1)
		if err != nil {
			t.Fatal(err)
		}
		if i != 1 {
			expected = append(expected, log[0])
			expected[len(expected)-1].Date = expected[len(expected)-1].Date.UTC()
		}
	}
	// Put the desired range behind many recent events, beyond the requested max.
	for i := 0; i < 15; i++ {
		old := f.env
		f.env = append(append([]string{}, old...), "GIT_AUTHOR_DATE=2025-06-01T00:00:00Z", "GIT_COMMITTER_DATE=2025-06-01T00:00:00Z")
		f.git(dir, "commit", "--allow-empty", "-q", "-m", fmt.Sprint(i))
		f.env = old
	}
	// Independent expected ordering for the two equal-date matches.
	if expected[1].Hash > expected[2].Hash {
		expected[1], expected[2] = expected[2], expected[1]
	}
	expected = []Commit{expected[1], expected[2], expected[0]}
	before := f.git(dir, "status", "--porcelain=v1")
	head := f.git(dir, "rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		start, end time.Time
		max        int
		want       []Commit
		cut        bool
	}{
		{from, to, 3, expected, false}, {from, to, 2, expected[:2], true},
		{from.Add(time.Nanosecond), to, 200, expected[:2], false},
		{from, from.Add(time.Second), 200, expected[2:], false},
		{from.In(time.FixedZone("fictional", 7*3600)), to, 200, expected, false},
	} {
		got, cut, err := f.g.LogRange(bg(), dir, tc.start, tc.end, tc.max)
		if err != nil || cut != tc.cut || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("range %s..%s: got %v %v %v; want %v %v", tc.start, tc.end, got, cut, err, tc.want, tc.cut)
		}
	}
	afterIndex, _ := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if before != f.git(dir, "status", "--porcelain=v1") || head != f.git(dir, "rev-parse", "HEAD") || string(index) != string(afterIndex) {
		t.Fatal("history read changed repository")
	}
}

func TestLogRangeValidationAndFailures(t *testing.T) {
	f := newFx(t)
	dir := f.repo("empty")
	from := rangeTime("2025-03-02T00:00:00Z")
	for _, tc := range []struct {
		a, b time.Time
		n    int
	}{
		{time.Time{}, from, 1}, {from, time.Time{}, 1}, {from, from, 1}, {from, from.Add(-time.Second), 1},
		{from, from.Add(logRangeDays + 1), 1}, {from, from.Add(time.Second), 0}, {from, from.Add(time.Second), 201},
	} {
		_, cut, err := f.g.LogRange(bg(), dir, tc.a, tc.b, tc.n)
		var ge *Error
		if !errors.Is(err, ErrInvalidArg) || !errors.As(err, &ge) || cut {
			t.Fatalf("invalid range: %v %v", cut, err)
		}
	}
	got, cut, err := f.g.LogRange(bg(), dir, from, from.Add(logRangeDays), 200)
	if err != nil || cut || len(got) != 0 {
		t.Fatalf("unborn: %v %v %v", got, cut, err)
	}
	ctx, cancel := context.WithCancel(bg())
	cancel()
	_, cut, err = f.g.LogRange(ctx, dir, from, from.Add(time.Hour), 1)
	var ge *Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &ge) || cut {
		t.Fatalf("cancel: %v %v", cut, err)
	}
	_, cut, err = f.g.LogRange(bg(), t.TempDir(), from, from.Add(time.Hour), 1)
	if !errors.As(err, &ge) || cut {
		t.Fatalf("non-repo: %v %v", cut, err)
	}
}

func rangeRecord(hash, date string) string {
	return strings.Join([]string{hash, hash[:7], "Fictional", "f@example.test", date, "", "subject"}, "\x1f") + "\x00"
}

func TestParseLogRangeCutoffAndMalformed(t *testing.T) {
	from := rangeTime("2025-03-02T00:00:00Z")
	to := from.Add(time.Hour)
	rec := rangeRecord(strings.Repeat("a", 40), from.Format(time.RFC3339Nano))
	for i := 0; i < len(rec); i++ {
		got, cut, err := parseLogRange(rec+rec[:i], true, from, to, 200)
		if err != nil || !cut || len(got) != 1 {
			t.Fatalf("cut at %d: %v %v %v", i, got, cut, err)
		}
	}
	for _, bad := range []string{"bad\x00", rec[:len(rec)-1], strings.Replace(rec, "2025-03-02", "not-a-date", 1), strings.Replace(rec, strings.Repeat("a", 40), "invalid", 1), strings.Replace(rec, "subject", "extra\x1ffield", 1)} {
		if _, _, err := parseLogRange(bad, false, from, to, 1); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if strings.HasSuffix(bad, "\x00") {
			if _, _, err := parseLogRange(bad, true, from, to, 1); err == nil {
				t.Fatalf("cutoff hid malformed complete record %q", bad)
			}
		}
	}
	// Count truncation must not conceal an unterminated record without byte cutoff.
	if _, _, err := parseLogRange(rec+rec+"broken", false, from, to, 1); err == nil {
		t.Fatal("count hid malformed record")
	}
}

func TestLogRangeByteBound(t *testing.T) {
	f := newFx(t)
	dir := f.repo("large")
	from := rangeTime("2025-03-02T00:00:00Z")
	// Large subjects exercise real output cancellation, including a partial record.
	subject := strings.Repeat("fictional ", 10000)
	f.env = append(f.env, "GIT_AUTHOR_DATE=2025-03-02T00:00:00Z", "GIT_COMMITTER_DATE=2025-06-01T00:00:00Z")
	for i := 0; i < 16; i++ {
		f.git(dir, "commit", "--allow-empty", "-q", "-m", subject+fmt.Sprint(i))
	}
	got, cut, err := f.g.LogRange(bg(), dir, from.Add(-24*time.Hour), from, 200)
	if err != nil || !cut || len(got) != 0 {
		t.Fatalf("bounded nonmatches: %d %v %v", len(got), cut, err)
	}
	got, cut, err = f.g.LogRange(bg(), dir, from, from.Add(time.Hour), 200)
	if err != nil || !cut || len(got) == 0 || len(got) >= 16 {
		t.Fatalf("bounded matches: %d %v %v", len(got), cut, err)
	}
	for _, c := range got {
		if !c.Date.Equal(from) || len(c.Subject) != len(subject)+1 && len(c.Subject) != len(subject)+2 {
			t.Fatal("fabricated partial record")
		}
	}

}

func FuzzParseLogRange(f *testing.F) {
	from := rangeTime("2025-03-02T00:00:00Z")
	to := from.Add(time.Hour)
	f.Add(rangeRecord(strings.Repeat("a", 40), from.Format(time.RFC3339Nano)), false)
	f.Add("partial", true)
	f.Fuzz(func(t *testing.T, s string, cut bool) {
		if len(s) > logRangeBytes {
			t.Skip()
		}
		got, truncated, err := parseLogRange(s, cut, from, to, 3)
		if err != nil {
			return
		}
		if len(got) > 3 || cut && !truncated {
			t.Fatal("bounds")
		}
		for i, c := range got {
			if c.Date.Before(from) || !c.Date.Before(to) || !validOID(c.Hash) || c.Date.Location() != time.UTC {
				t.Fatal("invalid accepted record")
			}
			if i > 0 && (got[i-1].Date.Before(c.Date) || got[i-1].Date.Equal(c.Date) && got[i-1].Hash > c.Hash) {
				t.Fatal("order")
			}
		}
	})
}

func TestPropLogRangeOrderAndBounds(t *testing.T) {
	from := rangeTime("2025-03-02T00:00:00.000000001Z")
	to := from.Add(5 * time.Second)
	rng := rand.New(rand.NewSource(51))
	for trial := 0; trial < 100; trial++ {
		var records []string
		var want []string
		dates := make(map[string]time.Time)
		for i := 0; i < 30; i++ {
			hash := fmt.Sprintf("%040x", i+1)
			at := from.Add(time.Duration(rng.Intn(10)-2) * time.Second)
			records = append(records, rangeRecord(hash, at.Format(time.RFC3339Nano)))
			if !at.Before(from) && at.Before(to) {
				want = append(want, hash)
				dates[hash] = at
			}
		}
		slices.SortFunc(want, func(a, b string) int {
			if dates[a].Equal(dates[b]) {
				return strings.Compare(a, b)
			}
			if dates[a].After(dates[b]) {
				return -1
			}
			return 1
		})
		n := 1 + rng.Intn(20)
		truncated := len(want) > n
		if truncated {
			want = want[:n]
		}
		rng.Shuffle(len(records), func(i, j int) { records[i], records[j] = records[j], records[i] })
		got, cut, err := parseLogRange(strings.Join(records, ""), false, from, to, n)
		var hashes []string
		for _, c := range got {
			hashes = append(hashes, c.Hash)
		}
		if err != nil || cut != truncated || !slices.Equal(hashes, want) {
			t.Fatalf("trial %d: got %v %v %v want %v %v", trial, hashes, cut, err, want, truncated)
		}
	}
}

func TestLogRangeCapturedBounds(t *testing.T) {
	f := newFx(t)
	dir := f.repo("capture")
	// Git produces a stable local value; exact fit is complete, overflow is partial.
	f.git(dir, "config", "fictional.value", "abcdefghij")
	for _, n := range []int{5, 11, 12} {
		got, cut, err := f.g.runCaptured(bg(), dir, "bounded config", true, n, 16, "config", "--get", "fictional.value")
		if err != nil || cut != (n < 11) || got != "abcdefghij\n"[:min(n, 11)] {
			t.Fatalf("cap %d: %q %v %v", n, got, cut, err)
		}
	}
	// A deadline owns the child lifetime and is classified, not an empty success.
	ctx, cancel := context.WithDeadline(bg(), time.Now().Add(-time.Second))
	defer cancel()
	_, cut, err := f.g.LogRange(ctx, dir, rangeTime("2025-03-02T00:00:00Z"), rangeTime("2025-03-03T00:00:00Z"), 1)
	if !errors.Is(err, context.DeadlineExceeded) || cut {
		t.Fatalf("deadline: %v %v", cut, err)
	}
}

func TestLogRangeReaderActiveCancellation(t *testing.T) {
	f := newFx(t)
	dir := f.repo("deadline")
	ctx, cancel := context.WithTimeout(bg(), 50*time.Millisecond)
	defer cancel()
	// A local shell alias stalls without IO; the context must end its process tree.
	_, cut, err := f.g.runCaptured(ctx, dir, "bounded wait", true, 100, 100, "-c", "alias.waiting=!while :; do :; done", "waiting")
	var ge *Error
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &ge) || cut {
		t.Fatalf("active deadline: %v %v", cut, err)
	}
}
