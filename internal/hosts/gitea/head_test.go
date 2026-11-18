package gitea

import (
	"strings"
	"testing"
)

func pullWithHead(sha string) string {
	return `[{"number":1,"title":"t","state":"open","merged":false,"updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a","sha":"` + sha + `"},"base":{"ref":"main"}}]`
}

func TestParsePullsHead(t *testing.T) {
	const sha = "4f2a9c1e7b3d5a60918c2e4d6f8a0b1c3d5e7f90"
	got, err := parsePulls([]byte(pullWithHead(sha)), "a")
	if err != nil || len(got) != 1 || got[0].Head != sha {
		t.Fatalf("valid head = %+v, %v", got, err)
	}
	got, err = parsePulls([]byte(pullWithHead("")), "a")
	if err != nil || len(got) != 1 || got[0].Head != "" {
		t.Fatalf("empty head = %+v, %v", got, err)
	}
	got, err = parsePulls([]byte("["+okPull+"]"), "a")
	if err != nil || len(got) != 1 || got[0].Head != "" {
		t.Fatalf("absent head = %+v, %v", got, err)
	}
	got, err = parsePulls([]byte(pullWithHead(strings.Repeat("a", 64))), "a")
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
		if got, err := parsePulls([]byte(pullWithHead(sha)), "a"); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
}

// Two refreshes with a different head give different values, so a stale pin
// is detectable.
func TestParsePullsHeadChangesBetweenRefreshes(t *testing.T) {
	a, err1 := parsePulls([]byte(pullWithHead(strings.Repeat("a", 40))), "a")
	b, err2 := parsePulls([]byte(pullWithHead(strings.Repeat("b", 40))), "a")
	if err1 != nil || err2 != nil || len(a) != 1 || len(b) != 1 || a[0].Head == b[0].Head {
		t.Fatalf("heads not told apart: %+v %+v (%v, %v)", a, b, err1, err2)
	}
}
