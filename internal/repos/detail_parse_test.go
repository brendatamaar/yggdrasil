package repos_test

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

func wantDetailInvalid(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted, want rejection", what)
	}
	if c, ok := apperr.ClassOf(err); !ok || c != apperr.Invalid {
		t.Fatalf("%s: class = %v, want Invalid (%v)", what, c, err)
	}
}

func TestParseRelPathRejects(t *testing.T) {
	for name, in := range map[string]string{
		"parent":               "../x",
		"inner parent":         "a/../../x",
		"backslash up":         `a\..\..\x`,
		"absolute":             "/etc/passwd",
		"windows root":         `\Windows`,
		"drive":                `C:\x`,
		"drive relative":       "C:x",
		"NUL":                  "a\x00b",
		"newline":              "a\nb",
		"option":               "-rf",
		"option in dir":        "./-x",
		"pathspec magic":       ":(top)x",
		"bare parent":          "..",
		"parent in the middle": "a/b/../../..",
	} {
		_, err := repos.ParseRelPath(in)
		wantDetailInvalid(t, err, name)
	}
}

func TestParseRelPathAccepts(t *testing.T) {
	for in, want := range map[string]string{
		"":                 "",
		".":                "",
		"a/b.txt":          "a/b.txt",
		`a\b.txt`:          "a/b.txt",
		"./a//b":           "a/b",
		"dir with space/f": "dir with space/f",
		"..x":              "..x",
	} {
		got, err := repos.ParseRelPath(in)
		if err != nil || got.String() != want {
			t.Errorf("ParseRelPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestParseRelPathIsIdempotent(t *testing.T) {
	p, err := repos.ParseRelPath(`./a\b//c/d`)
	if err != nil {
		t.Fatal(err)
	}
	q, err := repos.ParseRelPath(p.String())
	if err != nil || q != p {
		t.Fatalf("round trip: %q -> %q, %v", p, q, err)
	}
}

func FuzzParseRelPath(f *testing.F) {
	for _, s := range []string{"", "a/b", "../x", `C:\x`, "-x", ":(top)a", "a\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := repos.ParseRelPath(s)
		if err != nil {
			return
		}
		v := p.String()
		for seg := range strings.SplitSeq(v, "/") {
			if seg == ".." {
				t.Fatalf("accepted %q with .. segment: %q", s, v)
			}
		}
		if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "-") || strings.HasPrefix(v, ":") ||
			strings.ContainsAny(v, "\x00\n\\") || (len(v) > 1 && v[1] == ':') {
			t.Fatalf("accepted %q as invalid %q", s, v)
		}
		if q, err := repos.ParseRelPath(v); err != nil || q != p {
			t.Fatalf("round trip %q -> %q -> %q, %v", s, v, q, err)
		}
	})
}

func TestParseDiffSide(t *testing.T) {
	for in, want := range map[string]repos.DiffSide{"": repos.Unstaged, "unstaged": repos.Unstaged, "staged": repos.Staged} {
		got, err := repos.ParseDiffSide(in)
		if err != nil || got != want {
			t.Errorf("ParseDiffSide(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"Staged", "head", "all", " staged"} {
		_, err := repos.ParseDiffSide(in)
		wantDetailInvalid(t, err, in)
	}
}

func TestParseLimit(t *testing.T) {
	for in, want := range map[string]repos.Limit{"": 100, "1": 1, "500": 500, "42": 42} {
		got, err := repos.ParseLimit(in)
		if err != nil || got != want {
			t.Errorf("ParseLimit(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"0", "-1", "501", "abc", "1.5", " 5", "99999999999999999999"} {
		_, err := repos.ParseLimit(in)
		wantDetailInvalid(t, err, in)
	}
}

func FuzzParseLimit(f *testing.F) {
	for _, s := range []string{"", "0", "1", "500", "501", "x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		l, err := repos.ParseLimit(s)
		if err == nil && (l < 1 || l > repos.MaxLogLimit) {
			t.Fatalf("accepted %q as %d", s, l)
		}
	})
}

func TestParseRefRejects(t *testing.T) {
	for name, in := range map[string]string{
		"option": "-x", "double dot": "a..b", "space": "a b", "tilde": "a~1", "caret": "a^",
		"colon": "a:b", "question": "a?", "star": "a*", "bracket": "a[0]", "backslash": `a\b`,
		"reflog": "a@{1}", "at": "@", "lock": "a.lock", "lock segment": "a.lock/b", "trailing slash": "a/",
		"leading slash": "/a", "double slash": "a//b", "trailing dot": "a.", "hidden segment": "a/.b",
		"NUL": "a\x00", "control": "a\x07", "too long": strings.Repeat("a", 256),
	} {
		_, err := repos.ParseRef(in)
		wantDetailInvalid(t, err, name)
	}
}

func TestParseRefAccepts(t *testing.T) {
	for _, in := range []string{"", "main", "HEAD", "feature/x-1", "origin/main", "v1.2.3", "a@b"} {
		got, err := repos.ParseRef(in)
		if err != nil || got.String() != in {
			t.Errorf("ParseRef(%q) = %q, %v", in, got, err)
		}
	}
}

func FuzzParseRef(f *testing.F) {
	for _, s := range []string{"", "main", "-x", "a..b", "a b", "a/.b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := repos.ParseRef(s)
		if err != nil {
			return
		}
		if r.String() != s {
			t.Fatalf("changed %q to %q", s, r)
		}
		if strings.HasPrefix(s, "-") || strings.ContainsAny(s, " \x00\t\n~^:?*[\\") || strings.Contains(s, "..") {
			t.Fatalf("accepted invalid ref %q", s)
		}
	})
}

func TestParseOID(t *testing.T) {
	for in, want := range map[string]string{"abcd": "abcd", "ABCD1234": "abcd1234", strings.Repeat("f", 64): strings.Repeat("f", 64)} {
		got, err := repos.ParseOID(in)
		if err != nil || got.String() != want {
			t.Errorf("ParseOID(%q) = %q, %v", in, got, err)
		}
		if again, err := repos.ParseOID(got.String()); err != nil || again != got {
			t.Errorf("round trip %q failed", in)
		}
	}
	for name, in := range map[string]string{
		"short": "abc", "long": strings.Repeat("a", 65), "not hex": "abcg", "option": "--help", "empty": "",
		"revision": "HEAD~1", "NUL": "abcd\x00", "space": "abcd ",
	} {
		_, err := repos.ParseOID(in)
		wantDetailInvalid(t, err, name)
	}
}

func FuzzParseOID(f *testing.F) {
	for _, s := range []string{"abcd", "ABCD", "zz", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		o, err := repos.ParseOID(s)
		if err != nil {
			return
		}
		v := o.String()
		if len(v) < 4 || len(v) > 64 || strings.Trim(v, "0123456789abcdef") != "" {
			t.Fatalf("accepted %q as %q", s, v)
		}
	})
}
