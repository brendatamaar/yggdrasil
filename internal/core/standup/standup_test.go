package standup

import (
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestParseRange(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.FixedZone("x", 3600))
	r, err := ParseRange("prj_1", from, from.Add(24*time.Hour))
	if err != nil || r.From.Location() != time.UTC || !r.From.Equal(from) {
		t.Fatalf("ParseRange = %+v, %v", r, err)
	}
	if _, err := ParseRange("prj_1", from, from.Add(MaxRangeDays*24*time.Hour)); err != nil {
		t.Errorf("exactly %d days refused: %v", MaxRangeDays, err)
	}
	for name, c := range map[string][3]any{
		"inverted": {"prj_1", from.Add(time.Hour), from},
		"empty":    {"prj_1", from, from},
		"too long": {"prj_1", from, from.Add(MaxRangeDays*24*time.Hour + time.Second)},
		"no start": {"prj_1", time.Time{}, from},
		"bad id":   {"a b", from, from.Add(time.Hour)},
	} {
		if _, err := ParseRange(core.ProjectID(c[0].(string)), c[1].(time.Time), c[2].(time.Time)); !errors.Is(err, ErrInvalidRange) {
			t.Errorf("%s: %v, want ErrInvalidRange", name, err)
		}
	}
}
