package core

import (
	"strings"
	"testing"
)

func TestValidateHostInstanceAccepts(t *testing.T) {
	for _, v := range []string{
		"github.com", "git.acme.test", "git.acme.test:8443", "localhost:3000", "a", "a:1", "a:65535",
		"[::1]", "[::1]:8443", "git_host.acme-test", strings.Repeat("a", MaxHostInstanceLen),
	} {
		if err := ValidateHostInstance(v); err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
	}
}

func TestValidateHostInstanceRejectsEachRule(t *testing.T) {
	for name, v := range map[string]string{
		"empty":             "",
		"too long":          strings.Repeat("a", MaxHostInstanceLen+1),
		"upper case":        "GitHub.com",
		"invalid utf-8":     "g\xffit",
		"space":             "git acme",
		"tab":               "git.test\t",
		"newline":           "git.test\n",
		"nul":               "git.test\x00",
		"escape":            "git.test\x1b",
		"del":               "git.test\x7f",
		"c1 control":        "git.test\u0085",
		"slash path":        "git.test/acme",
		"backslash":         `git\acme`,
		"credentials":       "me@git.test",
		"scheme":            "https://git.test",
		"scheme no slashes": "https:",
		"query":             "git.test?x=1",
		"fragment":          "git.test#x",
		"empty port":        "git.test:",
		"port zero":         "git.test:0",
		"port too big":      "git.test:65536",
		"port not digits":   "git.test:80a",
		"port sign":         "git.test:+80",
		"port too long":     "git.test:000001",
		"no host":           ":8443",
		"two ports":         "git.test:1:2",
		"stray bracket":     "git]test",
		"open bracket":      "[::1",
		"empty brackets":    "[]",
		"text after ]":      "[::1]x",
	} {
		if err := ValidateHostInstance(v); err == nil {
			t.Errorf("%s: %q accepted", name, v)
		}
	}
}

func FuzzValidateHostInstance(f *testing.F) {
	for _, s := range []string{"git.acme.test:8443", "a", "", "[::1]:1", "x@y", "a:0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidateHostInstance(s) != nil {
			return
		}
		// Accepted implies the promised shape.
		if len(s) > MaxHostInstanceLen || s != strings.ToLower(s) || strings.ContainsAny(s, "@/\\ \t\r\n\x00?#") {
			t.Fatalf("accepted %q", s)
		}
	})
}

func TestCheckIDRefusesControlCharacters(t *testing.T) {
	for _, v := range []string{"req\x00", "req\x1b[31m", "req\x7f", "re\u0085q", "a\x01"} {
		if err := RequestID(v).Validate(); err == nil {
			t.Errorf("request id %q accepted", v)
		}
	}
	if err := RequestID("req_1-ok").Validate(); err != nil {
		t.Fatal(err)
	}
}
