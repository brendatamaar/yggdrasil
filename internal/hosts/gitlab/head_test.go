package gitlab

import (
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

func mrBody(extra string) string {
	return `[{"iid":1,"title":"t","state":"opened","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"main","web_url":"https://h/x/-/merge_requests/1"` + extra + `}]`
}

func TestParseMRsHead(t *testing.T) {
	const sha = "4f2a9c1e7b3d5a60918c2e4d6f8a0b1c3d5e7f90"
	other := strings.Repeat("c", 40)
	cases := map[string]struct{ extra, want string }{
		"sha":                  {`,"sha":"` + sha + `"`, sha},
		"diff_refs head_sha":   {`,"diff_refs":{"head_sha":"` + sha + `"}`, sha},
		"sha wins":             {`,"sha":"` + sha + `","diff_refs":{"head_sha":"` + other + `"}`, sha},
		"empty sha falls back": {`,"sha":"","diff_refs":{"head_sha":"` + other + `"}`, other},
		"empty":                {`,"sha":""`, ""},
		"absent":               {``, ""},
		"sha256":               {`,"sha":"` + strings.Repeat("a", 64) + `"`, strings.Repeat("a", 64)},
	}
	for name, c := range cases {
		got, err := parseMRs([]byte(mrBody(c.extra)))
		if err != nil || len(got) != 1 || got[0].Head != c.want {
			t.Errorf("%s: %+v, %v; want head %q", name, got, err, c.want)
		}
	}
}

func TestParseMRsRejectsEachHeadRule(t *testing.T) {
	for name, extra := range map[string]string{
		"short":                  `,"sha":"4f2a9c1"`,
		"upper case":             `,"sha":"` + strings.Repeat("A", 40) + `"`,
		"not hex":                `,"sha":"` + strings.Repeat("z", 40) + `"`,
		"41 digits":              `,"sha":"` + strings.Repeat("a", 41) + `"`,
		"bad diff_refs head_sha": `,"diff_refs":{"head_sha":"nope"}`,
	} {
		if got, err := parseMRs([]byte(mrBody(extra))); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
}

// Accepted implies valid: every head a parsed MR carries passes ParseHead.
func TestParseMRsAcceptedHeadIsValid(t *testing.T) {
	for _, sha := range []string{"", strings.Repeat("0", 40), strings.Repeat("f", 64), "abc", strings.Repeat("G", 40)} {
		got, err := parseMRs([]byte(mrBody(`,"sha":"` + sha + `"`)))
		if err != nil {
			continue
		}
		if _, err := corehosts.ParseHead(got[0].Head); err != nil {
			t.Errorf("accepted %q but ParseHead rejects %q: %v", sha, got[0].Head, err)
		}
	}
}

// Two refreshes with a different head give different values, so a stale pin
// is detectable.
func TestParseMRsHeadChangesBetweenRefreshes(t *testing.T) {
	a, err1 := parseMRs([]byte(mrBody(`,"sha":"` + strings.Repeat("a", 40) + `"`)))
	b, err2 := parseMRs([]byte(mrBody(`,"sha":"` + strings.Repeat("b", 40) + `"`)))
	if err1 != nil || err2 != nil || a[0].Head == b[0].Head {
		t.Fatalf("heads not told apart: %+v %+v (%v, %v)", a, b, err1, err2)
	}
}
