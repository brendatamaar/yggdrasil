package hosts

import "testing"

func TestSafeWebURL(t *testing.T) {
	for _, u := range []string{"https://git.acme.dev/x/-/merge_requests/1", "http://localhost:3000/a"} {
		if SafeWebURL(u) != u {
			t.Errorf("%q rejected", u)
		}
	}
	for _, u := range []string{"", "javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,x", "//h/x", "https://", "ftp://h/x", "u", "vbscript:x"} {
		if SafeWebURL(u) != "" {
			t.Errorf("%q accepted", u)
		}
	}
}
