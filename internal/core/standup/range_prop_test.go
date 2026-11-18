package standup

import (
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Every rejection rule of ParseRange on its own, including the one the table
// in standup_test.go leaves out (no end), each with a valid rest.
func TestParseRangeRejectsEachRuleAlone(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	for name, c := range map[string]struct {
		project  core.ProjectID
		from, to time.Time
	}{
		"empty project": {"", from, to},
		"no start":      {"prj_1", time.Time{}, to},
		"no end":        {"prj_1", from, time.Time{}},
		"end = start":   {"prj_1", from, from},
		"end < start":   {"prj_1", to, from},
		"one ns over":   {"prj_1", from, from.Add(MaxRangeDays*24*time.Hour + time.Nanosecond)},
	} {
		if r, err := ParseRange(c.project, c.from, c.to); !errors.Is(err, ErrInvalidRange) || r != (Range{}) {
			t.Errorf("%s: %+v, %v; want ErrInvalidRange and a zero range", name, r, err)
		}
	}
	// The half-open range includes its start and is exactly one nanosecond wide at least.
	if _, err := ParseRange("prj_1", from, from.Add(time.Nanosecond)); err != nil {
		t.Errorf("a one-nanosecond range: %v", err)
	}
}

// Random ranges: accepted if and only if the rules hold; an accepted range is
// UTC, half-open valid and parses to itself (round trip), whatever zone the
// caller used.
func TestPropParseRangeAcceptedIffValid(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2000; i++ {
		zone := time.FixedZone("z", (r.Intn(49)-24)*1800)
		from := base.Add(time.Duration(r.Int63n(int64(90 * 24 * time.Hour)))).In(zone)
		span := time.Duration(r.Int63n(int64(2*MaxRangeDays*24*time.Hour))) - 24*time.Hour
		to := from.Add(span)
		got, err := ParseRange("prj_1", from, to)
		wantOK := from.Before(to) && to.Sub(from) <= MaxRangeDays*24*time.Hour
		if (err == nil) != wantOK {
			t.Fatalf("from %v to %v: err %v, want ok=%v", from, to, err, wantOK)
		}
		if err != nil {
			continue
		}
		if got.From.Location() != time.UTC || got.To.Location() != time.UTC || !got.From.Equal(from) || !got.To.Equal(to) {
			t.Fatalf("not normalised to UTC or changed: %+v", got)
		}
		if again, err := ParseRange(got.Project, got.From, got.To); err != nil || again != got {
			t.Fatalf("round trip: %+v, %v", again, err)
		}
	}
}
