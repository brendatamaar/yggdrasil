package search

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestParseQueryAccepts(t *testing.T) {
	q, err := ParseQuery(RawQuery{Pattern: "  foo bar ", Repos: []string{"r1", "r1", "r2"}, Paths: []string{"*.java", "src/**/*.go", "*.java"}})
	if err != nil {
		t.Fatal(err)
	}
	if q.Mode != ModeLiteral || q.Pattern != "  foo bar " || len(q.Repos) != 2 || len(q.Paths) != 2 {
		t.Fatalf("%+v", q)
	}
}

func TestParseQueryOptionLikePatternIsData(t *testing.T) {
	for _, p := range []string{"-v", "--output=/tmp/x", "-e", "--open-files-in-pager=calc", "-"} {
		q, err := ParseQuery(RawQuery{Pattern: p})
		if err != nil || q.Pattern != p {
			t.Fatalf("%q: %+v %v", p, q, err)
		}
	}
}

func TestParseQueryRejections(t *testing.T) {
	cases := map[string]RawQuery{
		"empty":             {Pattern: ""},
		"blank":             {Pattern: " \t "},
		"oversize":          {Pattern: strings.Repeat("a", MaxPatternBytes+1)},
		"invalid utf8":      {Pattern: "a\xffb"},
		"nul":               {Pattern: "a\x00b"},
		"newline":           {Pattern: "a\nb"},
		"bad mode":          {Pattern: "a", Mode: "perl"},
		"bad repo id":       {Pattern: "a", Repos: []string{"a b"}},
		"empty repo id":     {Pattern: "a", Repos: []string{""}},
		"too many repos":    {Pattern: "a", Repos: make([]string, MaxRepoFilter+1)},
		"too many paths":    {Pattern: "a", Paths: make([]string, MaxPaths+1)},
		"empty path":        {Pattern: "a", Paths: []string{""}},
		"long path":         {Pattern: "a", Paths: []string{strings.Repeat("a", MaxPathBytes+1)}},
		"path dash":         {Pattern: "a", Paths: []string{"-x"}},
		"path colon magic":  {Pattern: "a", Paths: []string{":(top)*.go"}},
		"path exclude":      {Pattern: "a", Paths: []string{":!vendor"}},
		"path drive":        {Pattern: "a", Paths: []string{"C:/x"}},
		"path absolute":     {Pattern: "a", Paths: []string{"/etc/passwd"}},
		"path backslash":    {Pattern: "a", Paths: []string{"\\x"}},
		"path dotdot":       {Pattern: "a", Paths: []string{"../x"}},
		"path dotdot mid":   {Pattern: "a", Paths: []string{"a/../../x"}},
		"path dotdot win":   {Pattern: "a", Paths: []string{"a\\..\\x"}},
		"path control char": {Pattern: "a", Paths: []string{"a\nb"}},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			// Fill the placeholder empty strings of the "too many" cases with valid values.
			for i, s := range raw.Repos {
				if s == "" && name == "too many repos" {
					raw.Repos[i] = "r"
				}
			}
			for i, s := range raw.Paths {
				if s == "" && name == "too many paths" {
					raw.Paths[i] = "p"
				}
			}
			_, err := ParseQuery(raw)
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("want ErrInvalidQuery, got %v", err)
			}
		})
	}
}

func TestParseQueryRoundTrip(t *testing.T) {
	for _, raw := range []RawQuery{
		{Pattern: "x"},
		{Pattern: "a.*b", Mode: "regex", Case: true, Repos: []string{"r1", "r2"}, Paths: []string{"*.go"}},
	} {
		q, err := ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		q2, err := ParseQuery(q.Raw())
		if err != nil || !reflect.DeepEqual(q, q2) {
			t.Fatalf("round trip: %+v vs %+v (%v)", q, q2, err)
		}
		if err := q.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if (Query{Pattern: "x"}).Validate() == nil {
		t.Fatal("a query without a mode is not valid")
	}
}

// validQuery is an independent statement of what a parsed Query must satisfy.
// It deliberately does not call ParseQuery or Validate.
func validQuery(q Query) bool {
	if strings.TrimSpace(q.Pattern) == "" || len(q.Pattern) > MaxPatternBytes ||
		!utf8.ValidString(q.Pattern) || strings.ContainsAny(q.Pattern, "\x00\r\n") {
		return false
	}
	if q.Mode != ModeLiteral && q.Mode != ModeRegex {
		return false
	}
	if len(q.Repos) > MaxRepoFilter {
		return false
	}
	seenRepo := map[string]bool{}
	for _, id := range q.Repos {
		s := string(id)
		if s == "" || strings.ContainsAny(s, " \t\r\n/\\") || seenRepo[s] {
			return false
		}
		seenRepo[s] = true
	}
	if len(q.Paths) > MaxPaths {
		return false
	}
	seenPath := map[string]bool{}
	for _, p := range q.Paths {
		if strings.TrimSpace(p) == "" || len(p) > MaxPathBytes || !utf8.ValidString(p) ||
			strings.ContainsAny(p, "\x00\r\n") || strings.HasPrefix(p, "-") ||
			strings.Contains(p, ":") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") ||
			seenPath[p] {
			return false
		}
		seenPath[p] = true
		for _, seg := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
			if seg == ".." {
				return false
			}
		}
	}
	return true
}

func TestValidQueryOracleRejects(t *testing.T) {
	// The oracle itself must be able to say no.
	bad := []Query{
		{Pattern: "", Mode: ModeLiteral},
		{Pattern: "a", Mode: ""},
		{Pattern: "a", Mode: ModeLiteral, Paths: []string{"a/../b"}},
		{Pattern: "a", Mode: ModeLiteral, Paths: []string{`a\..\b`}},
		{Pattern: "a", Mode: ModeLiteral, Repos: []core.RepoID{"a/b"}},
	}
	for _, q := range bad {
		if validQuery(q) {
			t.Errorf("oracle accepted %+v", q)
		}
	}
}

func FuzzParseQuery(f *testing.F) {
	for _, s := range []string{"foo", "-v", "--output=x", "a\nb", "a\x00", strings.Repeat("a", 600), "é", ""} {
		f.Add(s, "literal", "*.go", "r1")
		f.Add(s, "regex", "*.go", "a b")
		f.Add(s, "", "*.go", "")
		f.Add(s, "regex", "../x", "r")
		f.Add(s, "regex", ":(top)", "r")
		f.Add(s, "regex", "a/../b", "r")
		f.Add(s, "regex", `a\..\b`, "r")
		f.Add(s, "regex", "-x", "r")
		f.Add(s, "regex", "/abs", "r")
		f.Add(s, "regex", "C:/x", "r")
	}
	f.Fuzz(func(t *testing.T, pattern, mode, path, repo string) {
		q, err := ParseQuery(RawQuery{Pattern: pattern, Mode: mode, Paths: []string{path}, Repos: []string{repo}})
		if err != nil {
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("unclassified error: %v", err)
			}
			return
		}
		// accepted => valid, by the independent predicate
		if !validQuery(q) {
			t.Fatalf("accepted an invalid query: %+v", q)
		}
		// round trip
		q2, err := ParseQuery(q.Raw())
		if err != nil || !reflect.DeepEqual(q, q2) {
			t.Fatalf("round trip failed: %+v %+v %v", q, q2, err)
		}
	})
}

func TestParseEditor(t *testing.T) {
	for _, s := range []string{"vscode", "cursor", "idea"} {
		if e, err := ParseEditor(s); err != nil || string(e) != s {
			t.Fatal(s, err)
		}
	}
	for _, s := range []string{"", "VSCode", "vim", "vscode://"} {
		if _, err := ParseEditor(s); err == nil {
			t.Fatalf("%q accepted", s)
		}
	}
}

func TestEditorURLExamples(t *testing.T) {
	cases := []struct {
		e         Editor
		path      string
		line, col int
		want      string
	}{
		{EditorVSCode, "/home/dev/shop api/A.java", 12, 3, "vscode://file/home/dev/shop%20api/A.java:12:3"},
		{EditorCursor, `C:\Users\dev\acme shop\a.go`, 5, 1, "cursor://file/C:/Users/dev/acme%20shop/a.go:5:1"},
		{EditorIdea, `C:\p\a.go`, 7, 2, "idea://open?file=C%3A/p/a.go&line=7"},
		{EditorVSCode, "/a/b.go", 0, -4, "vscode://file/a/b.go:1:1"},
	}
	for _, c := range cases {
		got, err := EditorURL(c.e, c.path, c.line, c.col)
		if err != nil || got != c.want {
			t.Errorf("%v %q: got %q (%v), want %q", c.e, c.path, got, err, c.want)
		}
	}
	if _, err := EditorURL("vim", "/a", 1, 1); err == nil {
		t.Error("unknown editor accepted")
	}
	if _, err := EditorURL(EditorIdea, " ", 1, 1); err == nil {
		t.Error("empty path accepted")
	}
}

// The link always parses as a URL with the right scheme and carries the path
// percent-encoded: decoding gives the path back.
func TestPropEditorURLParses(t *testing.T) {
	paths := []string{"/a b/c#d?e.go", `C:\x y\é&=.go`, "/p/%41/100%.go", "/a/b;c,d.go", "/ünï/日本.txt", `D:\a\b`}
	for _, e := range []Editor{EditorVSCode, EditorCursor, EditorIdea} {
		for _, p := range paths {
			got, err := EditorURL(e, p, 3, 4)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(got)
			if err != nil || u.Scheme != string(e) {
				t.Fatalf("%q: %v scheme %q", got, err, u.Scheme)
			}
			want := strings.ReplaceAll(p, `\`, "/")
			var dec string
			if e == EditorIdea {
				dec = u.Query().Get("file")
			} else {
				dec, _ = url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(got, string(e)+"://file/"), ":3:4"))
				want = strings.TrimLeft(want, "/")
			}
			if dec != want {
				t.Errorf("%v %q: decoded %q, want %q (url %q)", e, p, dec, want, got)
			}
			if strings.ContainsAny(strings.TrimPrefix(got, string(e)+"://"), " #") {
				t.Errorf("unescaped char in %q", got)
			}
		}
	}
}
