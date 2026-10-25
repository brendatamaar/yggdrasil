package git

import (
	"strings"
	"testing"
)

// Mutation-check tests: each pins a guard that no other test noticed when the
// guard was removed.

// A literal insert that runs past the end of the delta must be an error, not a
// slice-bounds panic. The delta has exact capacity so the slice cannot read
// into spare capacity.
func TestApplyDelta_TruncatedLiteralIsAnErrorNotAPanic(t *testing.T) {
	delta := make([]byte, 5)
	copy(delta, []byte{3, 10, 5, 'x', 'y'}) // base 3, result 10, literal of 5 but 2 present
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("applyDelta panicked on a truncated literal: %v", r)
		}
	}()
	if _, err := applyDelta([]byte("abc"), delta); err == nil {
		t.Fatal("truncated literal accepted")
	}
}

func TestParseCommitObject_ParentMustBe40LowerHex(t *testing.T) {
	oid := strings.Repeat("a", 40)
	mk := func(parent string) []byte {
		return []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nparent " + parent + "\nauthor " + goodAuthor + "\n\nx\n")
	}
	cases := []struct {
		name   string
		parent string
		ok     bool
	}{
		{"control", strings.Repeat("b", 40), true},
		{"too short but hex", strings.Repeat("b", 39), false},
		{"too long but hex", strings.Repeat("b", 41), false},
		{"40 chars, not hex", strings.Repeat("g", 40), false},
		{"40 chars, upper case", strings.Repeat("B", 40), false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseCommitObject(oid, mk(c.parent))
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want accepted=%v", err, c.ok)
			}
			if c.ok && (len(got.Parents) != 1 || got.Parents[0] != c.parent) {
				t.Errorf("Parents = %v", got.Parents)
			}
		})
	}
}

func TestParseIdent_TimezoneMustBeDigits(t *testing.T) {
	for _, tz := range []string{"+07x0", "-ab12", "+-700", "++700", "+07-0", "+0700"} {
		_, _, _, err := parseIdent("Ada Test <ada@acme-shop.example> 1700000000 " + tz)
		if want := tz == "+0700"; (err == nil) != want {
			t.Errorf("tz %q: err = %v, want accepted=%v", tz, err, want)
		}
	}
}
