package git

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestValidateRemoteURLAccepts(t *testing.T) {
	for in, want := range map[string]string{
		"https://git.example.test/acme/shop-api":      "https://git.example.test/acme/shop-api",
		"https://Git.Example.TEST/acme/shop-api.git":  "https://git.example.test/acme/shop-api.git",
		"https://git.example.test:8443/acme/shop-api": "https://git.example.test:8443/acme/shop-api",
		"HTTPS://git.example.test/acme/shop-api":      "https://git.example.test/acme/shop-api",
		"https://127.0.0.1:44321/acme/shop-api":       "https://127.0.0.1:44321/acme/shop-api",
		"https://git.example.test/acme/shop%7Eapi":    "https://git.example.test/acme/shop%7Eapi",
		"https://git.example.test/group/sub/shop-api": "https://git.example.test/group/sub/shop-api",
	} {
		got, err := ValidateRemoteURL(in)
		if err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
			continue
		}
		if again, err := ValidateRemoteURL(got); err != nil || again != got {
			t.Errorf("%q is not stable: %q, %v", got, again, err)
		}
	}
}

// One test per rejection rule.
func TestValidateRemoteURLRejects(t *testing.T) {
	long := "https://git.example.test/" + strings.Repeat("a", 3000)
	for name, c := range map[string]struct {
		in  string
		err error
	}{
		"empty":              {"", ErrBadRemoteURL},
		"encoded space":      {"https://git.example.test/a/b%20c", ErrBadRemoteURL},
		"too long":           {long, ErrBadRemoteURL},
		"userinfo":           {"https://user@git.example.test/a/b", ErrCredentialURL},
		"user and password":  {"https://user:pw@git.example.test/a/b", ErrCredentialURL},
		"at sign in path":    {"https://git.example.test/a/b@c", ErrCredentialURL},
		"query token":        {"https://git.example.test/a/b?access_token=x", ErrCredentialURL},
		"empty query":        {"https://git.example.test/a/b?", ErrCredentialURL},
		"fragment":           {"https://git.example.test/a/b#x", ErrCredentialURL},
		"http":               {"http://git.example.test/a/b", ErrBadRemoteURL},
		"ssh":                {"ssh://git.example.test/a/b", ErrBadRemoteURL},
		"file":               {"file:///tmp/a/b", ErrBadRemoteURL},
		"scp-like":           {"git@git.example.test:a/b", ErrCredentialURL},
		"no scheme":          {"git.example.test/a/b", ErrBadRemoteURL},
		"no host":            {"https:///a/b", ErrBadRemoteURL},
		"space":              {"https://git.example.test/a b", ErrBadRemoteURL},
		"control character":  {"https://git.example.test/a/b\n", ErrBadRemoteURL},
		"non-ascii":          {"https://git.example.test/a/bé", ErrBadRemoteURL},
		"backslash":          {`https://git.example.test\a\b`, ErrBadRemoteURL},
		"no path":            {"https://git.example.test", ErrBadRemoteURL},
		"root path":          {"https://git.example.test/", ErrBadRemoteURL},
		"trailing slash":     {"https://git.example.test/a/b/", ErrBadRemoteURL},
		"empty segment":      {"https://git.example.test/a//b", ErrBadRemoteURL},
		"dot segment":        {"https://git.example.test/a/./b", ErrBadRemoteURL},
		"dotdot":             {"https://git.example.test/a/../b", ErrBadRemoteURL},
		"encoded dotdot":     {"https://git.example.test/a/%2e%2e/b", ErrBadRemoteURL},
		"encoded slash":      {"https://git.example.test/a%2fb", ErrBadRemoteURL},
		"dot git segment":    {"https://git.example.test/a/.git", ErrBadRemoteURL},
		"bad port":           {"https://git.example.test:99999/a/b", ErrBadRemoteURL},
		"zero port":          {"https://git.example.test:0/a/b", ErrBadRemoteURL},
		"empty port":         {"https://git.example.test:/a/b", ErrBadRemoteURL},
		"leading zero port":  {"https://git.example.test:0443/a/b", ErrBadRemoteURL},
		"ipv6 literal":       {"https://[::1]/a/b", ErrBadRemoteURL},
		"host starts dash":   {"https://-evil.example.test/a/b", ErrBadRemoteURL},
		"host starts dot":    {"https://.example.test/a/b", ErrBadRemoteURL},
		"opaque":             {"https:git.example.test/a/b", ErrBadRemoteURL},
		"percent in host":    {"https://git%2eexample.test/a/b", ErrBadRemoteURL},
		"encoded nul":        {"https://git.example.test/a/b%00", ErrBadRemoteURL},
		"encoded newline":    {"https://git.example.test/a/b%0a", ErrBadRemoteURL},
		"encoded colon":      {"https://git.example.test/a/b%3ac", ErrBadRemoteURL},
		"encoded backslash":  {"https://git.example.test/a/b%5cc", ErrBadRemoteURL},
		"encoded at":         {"https://git.example.test/a/b%40c", ErrBadRemoteURL},
		"encoded question":   {"https://git.example.test/a/b%3fx", ErrBadRemoteURL},
		"windows device":     {`https://git.example.test/a/NUL:`, ErrBadRemoteURL},
		"ads like":           {"https://git.example.test/a/b:stream", ErrBadRemoteURL},
		"two schemes":        {"https://https://git.example.test/a/b", ErrBadRemoteURL},
		"credentials in uri": {"https://x-access-token:ghp_abc@git.example.test/a/b", ErrCredentialURL},
	} {
		_, err := ValidateRemoteURL(c.in)
		if err == nil {
			t.Errorf("%s: %q accepted", name, c.in)
			continue
		}
		if !errors.Is(err, c.err) {
			t.Errorf("%s: err = %v, want %v", name, err, c.err)
		}
		// The error never repeats the input, so a typed token is not echoed.
		for _, part := range []string{"ghp_abc", "pw@", "access_token"} {
			if strings.Contains(c.in, part) && strings.Contains(err.Error(), part) {
				t.Errorf("%s: error echoes %q", name, part)
			}
		}
	}
}

func FuzzValidateRemoteURL(f *testing.F) {
	for _, s := range []string{
		"https://git.example.test/acme/shop-api", "https://u:p@h/a/b", "http://h/a", "https://h:1/a?b#c",
		"https://h/a/%2e%2e/b", "ssh://h/a", "", "https://[::1]/a", "https://H/a%20b", "https://h/a\x00",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, err := ValidateRemoteURL(in)
		if err != nil {
			if !errors.Is(err, ErrBadRemoteURL) && !errors.Is(err, ErrCredentialURL) {
				t.Fatalf("unclassified error: %v", err)
			}
			if in != "" && len(in) > 8 && strings.Contains(err.Error(), in) {
				t.Fatalf("error echoes the input: %v", err)
			}
			return
		}
		// Accepted implies valid.
		u, perr := url.Parse(got)
		if perr != nil {
			t.Fatalf("accepted %q gives unparsable %q", in, got)
		}
		if u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Hostname() == "" {
			t.Fatalf("accepted %q gives a bad URL %q", in, got)
		}
		if strings.ContainsAny(got, "@?# \\") || strings.Contains(got, "/../") || strings.HasSuffix(got, "/..") || strings.Contains(got, "/./") {
			t.Fatalf("accepted %q gives a suspicious URL %q", in, got)
		}
		for _, c := range []byte(got) {
			if c <= 0x20 || c >= 0x7f {
				t.Fatalf("accepted %q gives control or non-ASCII byte in %q", in, got)
			}
		}
		// Round trip: the canonical form is a fixed point.
		if again, err := ValidateRemoteURL(got); err != nil || again != got {
			t.Fatalf("canonical form %q is not stable: %q, %v", got, again, err)
		}
	})
}
