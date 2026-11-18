package github

import (
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

func pullWithHead(sha string) string {
	return `[{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a","sha":"` + sha + `"},"base":{"ref":"main"}}]`
}

func TestParsePullsHead(t *testing.T) {
	const sha = "4f2a9c1e7b3d5a60918c2e4d6f8a0b1c3d5e7f90"
	got, err := parsePulls([]byte(pullWithHead(sha)))
	if err != nil || len(got) != 1 || got[0].Head != sha {
		t.Fatalf("valid head = %+v, %v", got, err)
	}
	got, err = parsePulls([]byte(pullWithHead("")))
	if err != nil || got[0].Head != "" {
		t.Fatalf("empty head = %+v, %v", got, err)
	}
	got, err = parsePulls([]byte("[" + okPull + "]"))
	if err != nil || got[0].Head != "" {
		t.Fatalf("absent head = %+v, %v", got, err)
	}
	got, err = parsePulls([]byte(pullWithHead(strings.Repeat("a", 64))))
	if err != nil || got[0].Head != strings.Repeat("a", 64) {
		t.Fatalf("sha256 head = %+v, %v", got, err)
	}
}

func TestParsePullsRejectsEachHeadRule(t *testing.T) {
	for name, sha := range map[string]string{
		"short":      "4f2a9c1",
		"upper case": strings.Repeat("A", 40),
		"not hex":    strings.Repeat("z", 40),
		"41 digits":  strings.Repeat("a", 41),
		"ref name":   "refs/heads/main",
	} {
		if got, err := parsePulls([]byte(pullWithHead(sha))); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
}

// Accepted implies valid: every head a parsed pull carries passes ParseHead.
func TestParsePullsAcceptedHeadIsValid(t *testing.T) {
	for _, sha := range []string{"", strings.Repeat("0", 40), strings.Repeat("f", 64), "abc", strings.Repeat("G", 40)} {
		got, err := parsePulls([]byte(pullWithHead(sha)))
		if err != nil {
			continue
		}
		if _, err := corehosts.ParseHead(got[0].Head); err != nil {
			t.Errorf("accepted %q but ParseHead rejects %q: %v", sha, got[0].Head, err)
		}
	}
}

// Search results (the inbox) carry no head; that is empty, not an error.
func TestParseSearchHasNoHead(t *testing.T) {
	body := `{"items":[{"number":7,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","repository_url":"https://api.h/repos/acme-shop/shop-web"}]}`
	items, err := parseSearch([]byte(body), corehosts.RoleAuthored)
	if err != nil || len(items) != 1 || items[0].MR.Head != "" {
		t.Fatalf("search = %+v, %v", items, err)
	}
}

// Two refreshes with a different head give different values, so a stale pin
// is detectable.
func TestParsePullsHeadChangesBetweenRefreshes(t *testing.T) {
	a, err1 := parsePulls([]byte(pullWithHead(strings.Repeat("a", 40))))
	b, err2 := parsePulls([]byte(pullWithHead(strings.Repeat("b", 40))))
	if err1 != nil || err2 != nil || a[0].Head == b[0].Head || a[0] == b[0] {
		t.Fatalf("heads not told apart: %+v %+v (%v, %v)", a, b, err1, err2)
	}
}
