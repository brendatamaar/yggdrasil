package hosts

import (
	"strings"
	"testing"
)

func TestParseHeadAccepts(t *testing.T) {
	for name, in := range map[string]string{
		"empty (host did not say)": "",
		"sha1":                     "4f2a9c1e7b3d5a60918c2e4d6f8a0b1c3d5e7f90",
		"sha256":                   strings.Repeat("ab12", 16),
		"all zero":                 strings.Repeat("0", 40),
	} {
		got, err := ParseHead(in)
		if err != nil || got != in {
			t.Errorf("%s: ParseHead(%q) = %q, %v", name, in, got, err)
		}
	}
}

func TestParseHeadRejectsEachRule(t *testing.T) {
	for name, in := range map[string]string{
		"too short":           "4f2a9c1",
		"39 digits":           strings.Repeat("a", 39),
		"41 digits":           strings.Repeat("a", 41),
		"63 digits":           strings.Repeat("a", 63),
		"65 digits":           strings.Repeat("a", 65),
		"upper case":          strings.Repeat("A", 40),
		"mixed case":          strings.Repeat("aB", 20),
		"not hex":             strings.Repeat("g", 40),
		"leading space":       " " + strings.Repeat("a", 39),
		"trailing newline":    strings.Repeat("a", 40) + "\n",
		"ref name":            "refs/heads/main",
		"very long":           strings.Repeat("a", 5000),
		"nul byte":            strings.Repeat("a", 39) + "\x00",
		"unicode look-alikes": strings.Repeat("а", 40),
	} {
		if got, err := ParseHead(in); err == nil {
			t.Errorf("%s: accepted %q as %q", name, in, got)
		} else if len(err.Error()) > 300 {
			t.Errorf("%s: error echoes the whole value (%d bytes)", name, len(err.Error()))
		}
	}
}

// accepted => valid: whatever ParseHead lets through is empty or a full
// lower case commit id, and parsing it again changes nothing.
func FuzzParseHead(f *testing.F) {
	for _, s := range []string{"", "abc", strings.Repeat("a", 40), strings.Repeat("F", 40), strings.Repeat("0", 64), "x\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseHead(s)
		if err != nil {
			return
		}
		if got != s {
			t.Fatalf("changed %q to %q", s, got)
		}
		if got != "" && !((len(got) == 40 || len(got) == 64) && strings.Trim(got, "0123456789abcdef") == "") {
			t.Fatalf("accepted %q", got)
		}
		if again, err := ParseHead(got); err != nil || again != got {
			t.Fatalf("not idempotent: %q -> %q, %v", got, again, err)
		}
	})
}
