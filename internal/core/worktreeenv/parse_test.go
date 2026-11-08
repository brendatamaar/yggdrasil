package worktreeenv

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseRangeRoundTrip(t *testing.T) {
	for _, r := range []Range{DefaultRange, {Lo: 1024, Hi: 1043}, {Lo: 40000, Hi: 65535}} {
		got, err := ParseRange(r.String())
		if err != nil || got != r {
			t.Errorf("%v -> %v, %v", r, got, err)
		}
	}
	if r, err := ParseRange(" 20000 - 29999 "); err != nil || r != DefaultRange {
		t.Errorf("spaces: %v %v", r, err)
	}
}

func TestParseRangeRejects(t *testing.T) {
	cases := map[string]error{
		"20000":       errRangeForm,
		"abc-def":     errRangeForm,
		"":            errRangeForm,
		"-5-30":       errRangeForm,
		"20000-":      errRangeForm,
		"+2000-3000":  errRangeForm,
		"1023-5000":   errRangeLow,
		"2000-65536":  errRangeHigh,
		"2000-2018":   errRangeSmall,
		"3000-2000":   errRangeSmall,
		"2000-2019x":  errRangeForm,
		"2000.5-3000": errRangeForm,
	}
	for in, want := range cases {
		if _, err := ParseRange(in); !errors.Is(err, want) {
			t.Errorf("%q: got %v, want %v", in, err, want)
		}
	}
	if _, err := ParseRange("2000-2019"); err != nil {
		t.Errorf("exactly 20 ports must be accepted: %v", err)
	}
}

func TestParseProfileRoundTripAndNormal(t *testing.T) {
	in := []string{`config\local.env`, ".env", "./a//b/c.txt", "CONFIG/LOCAL.env", "dir/"}
	got, err := ParseProfile(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []RelPath{"config/local.env", ".env", "a/b/c.txt", "dir"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	strs := make([]string, len(got))
	for i, p := range got {
		strs[i] = string(p)
		if err := p.Validate(); err != nil {
			t.Errorf("accepted %q but Validate says %v", p, err)
		}
	}
	again, err := ParseProfile(strs)
	if err != nil || fmt.Sprint(again) != fmt.Sprint(got) {
		t.Fatalf("round trip: %v %v", again, err)
	}
}

func TestParseProfileRejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrPathEmpty},
		{"blank", "   ", ErrPathEmpty},
		{"dot only", "./", ErrPathEmpty},
		{"absolute unix", "/etc/passwd", ErrPathAbsolute},
		{"absolute backslash", `\Windows\x`, ErrPathAbsolute},
		{"drive letter", `C:\x.env`, ErrPathAbsolute},
		{"drive relative", "C:x", ErrPathAbsolute},
		{"unc", `\\host\share\f`, ErrPathAbsolute},
		{"stream", "a.txt:stream", ErrPathAbsolute},
		{"parent", "../x", ErrPathParent},
		{"parent inside", "a/../b", ErrPathParent},
		{"parent backslash", `a\..\..\b`, ErrPathParent},
		{"git", ".git/config", ErrPathGit},
		{"git upper", "sub/.GIT/hooks/x", ErrPathGit},
		{"git short name", "GIT~1/config", ErrPathGit},
		{"nul byte", "a\x00b", ErrPathControl},
		{"newline", "a\nb", ErrPathControl},
		{"glob star", "*.env", ErrPathGlob},
		{"glob question", "a?.env", ErrPathGlob},
		{"glob bracket", "a[0].env", ErrPathGlob},
		{"reserved", "con", ErrPathReserved},
		{"reserved ext", "dir/NUL.txt", ErrPathReserved},
		{"reserved com", "com3", ErrPathReserved},
		{"reserved lpt", "lpt9.log", ErrPathReserved},
		{"trailing dot", "a.", ErrPathReserved},
		{"trailing space", "a /b", ErrPathReserved},
		{"bad char", "a<b", ErrPathReserved},
		{"too long", strings.Repeat("a", 261), ErrPathLong},
		{"invalid utf8", "a\xffb", ErrPathControl},
	}
	for _, c := range cases {
		if _, err := ParseProfile([]string{c.in}); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	var many []string
	for i := range MaxEntries + 1 {
		many = append(many, fmt.Sprintf("f%d", i))
	}
	if _, err := ParseProfile(many); !errors.Is(err, ErrPathCount) {
		t.Errorf("too many entries: %v", err)
	}
	if got, err := ParseProfile(many[:MaxEntries]); err != nil || len(got) != MaxEntries {
		t.Errorf("50 entries must pass: %v", err)
	}
	if got, err := ParseProfile(nil); err != nil || len(got) != 0 {
		t.Errorf("empty list: %v %v", got, err)
	}
	// an accepted name that merely looks like a reserved one
	if _, err := ParseProfile([]string{"console.log", "com10", ".github/x", "gitignore"}); err != nil {
		t.Errorf("false rejection: %v", err)
	}
}

func TestBlockRows(t *testing.T) {
	if b, err := BlockFromRow(20000, 20); err != nil || RowOfPair(b) != [2]int{20000, 20} {
		t.Fatalf("%v %v", b, err)
	}
	if _, err := BlockFromRow(0, 20); !errors.Is(err, ErrBlockRange) {
		t.Error("base 0")
	}
	if _, err := BlockFromRow(65530, 20); !errors.Is(err, ErrBlockRange) {
		t.Error("past 65535")
	}
	if _, err := BlockFromRow(20005, 7); err != nil {
		t.Errorf("legacy rows must be readable: %v", err)
	}
	r := DefaultRange
	if _, err := NewBlockFromRow(20010, 20, r); !errors.Is(err, ErrBlockAlign) {
		t.Error("misaligned")
	}
	if _, err := NewBlockFromRow(20000, 10, r); !errors.Is(err, ErrBlockSize) {
		t.Error("size")
	}
	if _, err := NewBlockFromRow(29980, 20, r); err != nil {
		t.Errorf("last block: %v", err)
	}
	if _, err := NewBlockFromRow(30000, 20, r); !errors.Is(err, ErrBlockOutOf) {
		t.Error("outside range")
	}
}

// RowOfPair adapts RowOf for comparison in tests.
func RowOfPair(b Block) [2]int { base, size := RowOf(b); return [2]int{base, size} }

func FuzzParseRange(f *testing.F) {
	for _, s := range []string{"20000-29999", "", "-", "1-2", "99999999999999999999-1", " 1024 - 1100 "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := ParseRange(s)
		if err != nil {
			return
		}
		if r.Validate() != nil {
			t.Fatalf("accepted invalid %v", r)
		}
		if again, err := ParseRange(r.String()); err != nil || again != r {
			t.Fatalf("round trip %v: %v %v", r, again, err)
		}
	})
}

func FuzzParseProfile(f *testing.F) {
	for _, s := range []string{".env", `a\b`, "../x", "C:\\x", ".git/x", "a/./b", "CON", "a\x00", "dir/"} {
		f.Add(s, "other")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		got, err := ParseProfile([]string{a, b})
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, p := range got {
			if err := p.Validate(); err != nil {
				t.Fatalf("accepted %q but invalid: %v", p, err)
			}
			s := string(p)
			if strings.HasPrefix(s, "/") || strings.Contains(s, `\`) {
				t.Fatalf("not normal: %q", s)
			}
			for _, c := range strings.Split(s, "/") {
				if c == ".." || c == "." || c == "" || strings.EqualFold(c, ".git") {
					t.Fatalf("bad component in %q", s)
				}
			}
			if seen[strings.ToLower(s)] {
				t.Fatalf("duplicate %q", s)
			}
			seen[strings.ToLower(s)] = true
		}
		strs := make([]string, len(got))
		for i, p := range got {
			strs[i] = string(p)
		}
		again, err := ParseProfile(strs)
		if err != nil || fmt.Sprint(again) != fmt.Sprint(got) {
			t.Fatalf("round trip %v: %v %v", got, again, err)
		}
	})
}
