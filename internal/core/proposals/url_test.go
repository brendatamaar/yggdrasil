package proposals

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeWebURL(t *testing.T) {
	ok := []string{
		"https://gitlab.example.com/acme/shop-api/-/merge_requests/7",
		"http://localhost:8080/x?a=b#c",
		"HTTPS://Host.Example/x",
		"https://[::1]:8443/x",
		"https://example.com",
	}
	bad := map[string]string{
		"empty": "", "no scheme": "gitlab.example.com/x", "ftp": "ftp://h/x", "javascript": "javascript:alert(1)",
		"file": "file:///etc/passwd", "user": "https://user@host/x", "password": "https://user:pw@host/x",
		"token only": "https://glpat-abc@host/x", "at in path-looking host": "https://host@evil.com/x",
		"no host": "https:///x", "empty host query": "https://?x", "space": "https://host/a b", "newline": "https://host/a\nb",
		"backslash": `https://host\@evil/x`, "bad port": "https://host:80a/x", "empty port": "https://host:/x",
		"bad host char": "https://ho st/x", "unclosed v6": "https://[::1/x", "tab": "https://host/\tx",
		"scheme only": "https://", "port only": "https://:80/x", "NUL": "https://host/\x00",
	}
	for _, u := range ok {
		if !SafeWebURL(u) {
			t.Errorf("SafeWebURL(%q) = false", u)
		}
	}
	for name, u := range bad {
		if SafeWebURL(u) {
			t.Errorf("%s: SafeWebURL(%q) = true", name, u)
		}
	}
}

func TestParsePushOutput(t *testing.T) {
	good := "remote: \nremote: View merge request for ygg/x:\nremote:   https://gitlab.example.com/acme/shop-api/-/merge_requests/7\nremote: \nTo origin\n"
	r, err := ParsePushOutput(good)
	if err != nil || r.MRURL != "https://gitlab.example.com/acme/shop-api/-/merge_requests/7" {
		t.Fatalf("got %+v, %v", r, err)
	}
	r, err = ParsePushOutput("remote: View merge request for x: https://gl.example/m/1.\r\n")
	if err != nil || r.MRURL != "https://gl.example/m/1" {
		t.Fatalf("same line, CRLF: %+v, %v", r, err)
	}
	for name, out := range map[string]string{
		"empty":         "",
		"no link":       "remote: Resolving deltas: 100%\nTo origin\n",
		"hint only":     "remote: To create a merge request for x, visit:\nremote:   https://gl.example/m/new\n",
		"marker no url": "remote: View merge request for x:\nremote: nothing\n",
	} {
		if _, err := ParsePushOutput(out); !errors.Is(err, ErrNoMRURL) {
			t.Errorf("%s: err = %v, want ErrNoMRURL", name, err)
		}
	}
	for name, u := range map[string]string{
		"userinfo":     "https://user:token@gl.example/m/1",
		"token":        "https://glpat-xyz@gl.example/m/1",
		"bad host":     "https://:80/m/1",
		"ftp":          "",
		"no authority": "https:///m/1",
	} {
		if u == "" {
			continue
		}
		_, err := ParsePushOutput("remote: View merge request for x:\nremote:   " + u + "\n")
		if !errors.Is(err, ErrUnsafeURL) {
			t.Errorf("%s: err = %v, want ErrUnsafeURL", name, err)
		}
	}
}

func TestCheckRemote(t *testing.T) {
	ok := map[string]Remote{
		"https://gitlab.example.com/acme/shop-api.git": {"https", "gitlab.example.com", "/acme/shop-api.git"},
		"http://localhost:3000/x.git":                  {"http", "localhost:3000", "/x.git"},
		"ssh://git@gitlab.example.com:2222/acme/x.git": {"ssh", "gitlab.example.com:2222", "/acme/x.git"},
		"ssh://gitlab.example.com/acme/x.git":          {"ssh", "gitlab.example.com", "/acme/x.git"},
		"git@gitlab.example.com:acme/shop-api.git":     {"scp", "gitlab.example.com", "acme/shop-api.git"},
	}
	for in, want := range ok {
		got, err := CheckRemote(in)
		if err != nil || got != want {
			t.Errorf("CheckRemote(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	bad := map[string]string{
		"empty": "", "http password": "https://user:pw@host/x.git", "http token": "https://glpat-abc@host/x.git",
		"http user": "https://user@host/x.git", "ssh password": "ssh://git:pw@host/x.git", "ssh token user": "ssh://a%40b@host/x",
		"scp password": "git:pw@host:x.git", "file": "file:///srv/repo", "ext": "ext::sh -c evil", "local": "../repo",
		"windows drive": `C:\repos\x`, "drive slash": "C:/repos/x", "option": "--upload-pack=evil", "git proto": "git://host/x.git",
		"query token": "https://host/x.git?token=abc", "fragment": "https://host/x.git#a", "space": "https://host/a b",
		"scp no path": "git@host:", "scp bad user": "a b@host:x", "no host": "https:///x", "scp newline": "git@host:x\n",
	}
	for name, in := range bad {
		if _, err := CheckRemote(in); !errors.Is(err, ErrUnsafeURL) {
			t.Errorf("%s: CheckRemote(%q) err = %v", name, in, err)
		}
	}
}

func TestErrorsAreFree(t *testing.T) {
	// the error text never echoes the rejected URL (it may hold a token)
	_, err := CheckRemote("https://glpat-secret123@host/x")
	if err == nil || strings.Contains(err.Error(), "secret123") {
		t.Fatalf("error leaks or is nil: %v", err)
	}
}
