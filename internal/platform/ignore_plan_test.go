package platform_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func TestPlanIgnoreAppend(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		entries  []string
		want     string // bytes to add; "<none>" for nil
	}{
		{"empty file", "", []string{"a", "b"}, "a\nb\n"},
		{"LF final newline", "x\n", []string{"a"}, "a\n"},
		{"LF no final newline", "x", []string{"a", "b"}, "\na\nb"},
		{"CRLF", "x\r\ny\r\n", []string{"a"}, "a\r\n"},
		{"CRLF no final newline", "x\r\ny", []string{"a"}, "\r\na"},
		{"mixed, more CRLF", "x\r\ny\r\nz\n", []string{"a"}, "a\r\n"},
		{"mixed, more LF", "x\ny\nz\r\n", []string{"a"}, "a\n"},
		{"present exactly", "a\nb\n", []string{"b", "a"}, "<none>"},
		{"present with CRLF", "a\r\n", []string{"a"}, "<none>"},
		{"present on unfinished last line", "a\nb", []string{"b"}, "<none>"},
		{"similar is not present", "a/\n", []string{"a"}, "a\n"},
		{"comment is not present", "#a\n", []string{"a"}, "a\n"},
		{"trailing space is not present", "a \n", []string{"a"}, "a\n"},
		{"duplicates once", "x\n", []string{"a", "a", "b"}, "a\nb\n"},
		{"blank lines kept as is", "x\n\n\n", []string{"a"}, "a\n"},
		{"lone CR at the end", "x\r", []string{"a"}, "\na"},
	}
	for _, c := range cases {
		got, err := platform.PlanIgnoreAppend([]byte(c.existing), c.entries)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if c.want == "<none>" {
			if got != nil {
				t.Errorf("%s: got %q, want nothing", c.name, got)
			}
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPlanIgnoreAppendRejectsBadEntries(t *testing.T) {
	for _, e := range []string{"", "a\nb", "a\rb", "a\x00", string(bytes.Repeat([]byte("x"), 4097))} {
		if _, err := platform.PlanIgnoreAppend(nil, []string{e}); !errors.Is(err, platform.ErrInvalidArgument) {
			t.Errorf("%q: %v", e, err)
		}
	}
}

// Accepted entries always end up as exact lines, a second plan is empty, and
// the existing bytes are never part of the output.
func FuzzPlanIgnoreAppend(f *testing.F) {
	f.Add("a\r\nb", "a", "c")
	f.Add("", "x", "x")
	f.Fuzz(func(t *testing.T, existing, e1, e2 string) {
		add, err := platform.PlanIgnoreAppend([]byte(existing), []string{e1, e2})
		if err != nil {
			return
		}
		result := append([]byte(existing), add...)
		again, err := platform.PlanIgnoreAppend(result, []string{e1, e2})
		if err != nil || again != nil {
			t.Fatalf("not idempotent: %q + %q -> %q, %v", existing, add, again, err)
		}
	})
}
