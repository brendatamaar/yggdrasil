package hosts

import "testing"

func TestSafeWebURL(t *testing.T) {
	for _, u := range []string{"https://git.acme.dev/x/-/merge_requests/1", "http://localhost:3000/a", "HTTPS://git.acme.dev/x"} {
		if SafeWebURL(u) != u {
			t.Errorf("%q rejected", u)
		}
	}
	for _, u := range []string{
		"", "javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,x", "//h/x", "https://", "ftp://h/x", "u", "vbscript:x",
		" https://h/x", "https://h/\nx", "http:foo", "https://user:pw@h/", "https://user@h/",
	} {
		if SafeWebURL(u) != "" {
			t.Errorf("%q accepted", u)
		}
	}
}
