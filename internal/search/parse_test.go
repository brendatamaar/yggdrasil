package search_test

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/search"
)

func TestParseRequestRoundTrip(t *testing.T) {
	for _, raw := range []coresearch.RawQuery{
		{Pattern: "needle"},
		{Pattern: "ne+dle|x[0-9]", Mode: "regex", Case: true, Repos: []string{"rep_a", "rep_b"}, Paths: []string{"*.java", "src/**/*.go"}},
		{Pattern: "--output=x", Mode: "literal"},
	} {
		q, err := search.ParseRequest(raw)
		if err != nil {
			t.Fatalf("%+v: %v", raw, err)
		}
		again, err := search.ParseRequest(q.Raw())
		if err != nil || again.Pattern != q.Pattern || again.Mode != q.Mode || again.Case != q.Case ||
			strings.Join(again.Paths, "|") != strings.Join(q.Paths, "|") || len(again.Repos) != len(q.Repos) {
			t.Errorf("parse(format(q)) = %+v, %v; want %+v", again, err, q)
		}
	}
}

func TestParseRequestRejectsEachRule(t *testing.T) {
	cases := map[string]coresearch.RawQuery{
		"empty pattern":      {Pattern: "  "},
		"pattern too long":   {Pattern: strings.Repeat("a", coresearch.MaxPatternBytes+1)},
		"newline in pattern": {Pattern: "a\nb"},
		"unknown mode":       {Pattern: "a", Mode: "glob"},
		"bad repo id":        {Pattern: "a", Repos: []string{"../x"}},
		"glob dash":          {Pattern: "a", Paths: []string{"-x"}},
		"glob parent":        {Pattern: "a", Paths: []string{"../x"}},
		"glob magic":         {Pattern: "a", Paths: []string{":(top)x"}},
		"regex unclosed":     {Pattern: "(ab", Mode: "regex"},
		"regex bad repeat":   {Pattern: "*a", Mode: "regex"},
		"regex bad class":    {Pattern: "[a", Mode: "regex"},
		"regex group flags":  {Pattern: "(?i)a", Mode: "regex"},
	}
	for name, raw := range cases {
		_, err := search.ParseRequest(raw)
		if class, ok := apperr.ClassOf(err); !ok || class != apperr.Invalid {
			t.Errorf("%s: err = %v, want an invalid-request error", name, err)
		}
		if msg, _ := apperr.MessageOf(err); msg == "" || strings.Contains(msg, "invalid search:") {
			t.Errorf("%s: message %q is not plain language", name, msg)
		}
	}
	// The same unbalanced pattern is fine as plain text.
	if _, err := search.ParseRequest(coresearch.RawQuery{Pattern: "(ab"}); err != nil {
		t.Errorf("literal \"(ab\": %v", err)
	}
}

func FuzzParseRequest(f *testing.F) {
	f.Add("needle", "literal", "*.go", "rep_a")
	f.Add("(a|b)+", "regex", "src/x", "")
	f.Add("--x", "regex", "-x", "../")
	f.Fuzz(func(t *testing.T, pattern, mode, path, repo string) {
		raw := coresearch.RawQuery{Pattern: pattern, Mode: mode, Case: true}
		if path != "" {
			raw.Paths = []string{path}
		}
		if repo != "" {
			raw.Repos = []string{repo}
		}
		q, err := search.ParseRequest(raw)
		if err != nil {
			return
		}
		// accepted => valid
		if q.Validate() != nil || search.CheckRegex(q) != nil {
			t.Fatalf("accepted an invalid query %+v", q)
		}
		for _, p := range q.Paths {
			if strings.HasPrefix(p, "-") || strings.Contains(p, "..") && strings.Contains(p, "/..") {
				t.Fatalf("accepted path filter %q", p)
			}
		}
		again, err := search.ParseRequest(q.Raw())
		if err != nil || again.Pattern != q.Pattern {
			t.Fatalf("round trip failed for %+v: %v", q, err)
		}
	})
}
