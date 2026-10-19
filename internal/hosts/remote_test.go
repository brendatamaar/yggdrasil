package hosts

import (
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

func TestParseRemoteAccepts(t *testing.T) {
	cases := []struct {
		raw  string
		want RemoteRef
	}{
		{"https://git.acme.dev/acme-shop/shop-api.git", RemoteRef{Scheme: "https", Host: "git.acme.dev", Project: "acme-shop/shop-api"}},
		{"https://GitHub.com/acme-shop/shop-api", RemoteRef{Scheme: "https", Host: "github.com", Project: "acme-shop/shop-api"}},
		{"https://git.acme.dev:8443/group/sub/shop-api.git/", RemoteRef{Scheme: "https", Host: "git.acme.dev", Port: 8443, Project: "group/sub/shop-api"}},
		{"git@git.acme.dev:acme-shop/shop-api.git", RemoteRef{Scheme: "ssh", Host: "git.acme.dev", Project: "acme-shop/shop-api"}},
		{"ssh://git@git.acme.dev:2222/acme-shop/shop-api.git", RemoteRef{Scheme: "ssh", Host: "git.acme.dev", Port: 2222, Project: "acme-shop/shop-api"}},
		{"git+ssh://git@github.com/acme-shop/shop-api", RemoteRef{Scheme: "ssh", Host: "github.com", Project: "acme-shop/shop-api"}},
		{"http://localhost:3000/acme-shop/shop-api", RemoteRef{Scheme: "http", Host: "localhost", Port: 3000, Project: "acme-shop/shop-api"}},
		{"https://oauth2:tok-123@git.acme.dev/acme-shop/shop-api.git", RemoteRef{Scheme: "https", Host: "git.acme.dev", Project: "acme-shop/shop-api", HadCredentials: true}},
		{"https://tok-123@git.acme.dev/acme-shop/shop-api.git", RemoteRef{Scheme: "https", Host: "git.acme.dev", Project: "acme-shop/shop-api", HadCredentials: true}},
		{"ssh://git:pw@git.acme.dev/acme-shop/shop-api.git", RemoteRef{Scheme: "ssh", Host: "git.acme.dev", Project: "acme-shop/shop-api", HadCredentials: true}},
	}
	for _, c := range cases {
		got, err := ParseRemote(c.raw)
		if err != nil || got != c.want {
			t.Errorf("ParseRemote(%q) = %+v, %v; want %+v", c.raw, got, err, c.want)
		}
	}
}

func TestParseRemoteRejectsEachRule(t *testing.T) {
	cases := map[string]string{
		"empty":                "",
		"spaces":               "https://git.acme.dev/a b/c",
		"file URL":             "file:///srv/git/shop-api.git",
		"unix path":            "/srv/git/shop-api.git",
		"relative path":        "../shop-api",
		"home path":            "~/shop-api",
		"drive letter":         `C:\repos\shop-api`,
		"drive letter slash":   "C:/repos/shop-api",
		"other scheme":         "ftp://git.acme.dev/acme-shop/shop-api",
		"git scheme":           "git://git.acme.dev/acme-shop/shop-api",
		"plain http non-local": "http://git.acme.dev/acme-shop/shop-api",
		"no host":              "https:///acme-shop/shop-api",
		"empty project":        "https://git.acme.dev/",
		"one segment":          "https://git.acme.dev/shop-api",
		"dot dot":              "https://git.acme.dev/acme-shop/../shop-api",
		"dot dot scp":          "git@git.acme.dev:acme-shop/../x",
		"query":                "https://git.acme.dev/acme-shop/shop-api?x=1",
		"bad port":             "https://git.acme.dev:99999/acme-shop/shop-api",
		"no colon":             "shop-api",
		"scp no host":          ":acme-shop/shop-api",
		"empty segment":        "https://git.acme.dev/acme-shop//shop-api",
	}
	for name, raw := range cases {
		if got, err := ParseRemote(raw); err == nil {
			t.Errorf("%s: %q accepted as %+v", name, raw, got)
		}
	}
}

func TestParseRemoteErrorsNeverEchoTheURL(t *testing.T) {
	for _, raw := range []string{"ftp://u:SECRET-pw@git.acme.dev/a/b", "https://u:SECRET-pw@git.acme.dev/", "/srv/SECRET-pw"} {
		_, err := ParseRemote(raw)
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("ParseRemote(%q) error = %v", raw, err)
		}
	}
}

func TestRemoteRoundTrip(t *testing.T) {
	refs := []RemoteRef{
		{Scheme: "https", Host: "git.acme.dev", Project: "acme-shop/shop-api"},
		{Scheme: "https", Host: "git.acme.dev", Port: 8443, Project: "g/s/shop-api"},
		{Scheme: "ssh", Host: "github.com", Project: "acme-shop/shop-api"},
		{Scheme: "ssh", Host: "git.acme.dev", Port: 2222, Project: "acme-shop/shop-api"},
		{Scheme: "http", Host: "localhost", Port: 3000, Project: "a/b"},
	}
	for _, r := range refs {
		got, err := ParseRemote(r.Format())
		if err != nil || got != r {
			t.Errorf("round trip of %+v via %q = %+v, %v", r, r.Format(), got, err)
		}
	}
	scp := RemoteRef{Scheme: "ssh", Host: "git.acme.dev", Project: "acme-shop/shop-api"}
	if got, err := ParseRemote(scp.FormatSCP()); err != nil || got != scp {
		t.Errorf("scp round trip = %+v, %v", got, err)
	}
}

func TestRemoteInstanceID(t *testing.T) {
	cases := map[string]corehosts.InstanceID{
		"https://git.acme.dev:8443/a/b":   "git.acme.dev:8443",
		"https://git.acme.dev:443/a/b":    "git.acme.dev",
		"ssh://git@git.acme.dev:2222/a/b": "git.acme.dev",
		"git@git.acme.dev:a/b":            "git.acme.dev",
		"http://localhost:3000/a/b":       "localhost:3000",
	}
	for raw, want := range cases {
		ref, err := ParseRemote(raw)
		if err != nil || ref.InstanceID() != want {
			t.Errorf("%q: instance = %q, %v; want %q", raw, ref.InstanceID(), err, want)
		}
	}
}

func FuzzParseRemote(f *testing.F) {
	for _, s := range []string{"https://git.acme.dev/a/b.git", "git@h:a/b", "ssh://git@h:22/a/b", "C:\\x", "https://u:p@h/a/b", "", "http://[::1]/a/b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		ref, err := ParseRemote(raw)
		if err != nil {
			if strings.Contains(err.Error(), raw) && len(raw) > 3 {
				t.Fatalf("error echoes the input: %v", err)
			}
			return
		}
		// accepted => valid
		if ref.Host == "" || ref.Host != strings.ToLower(ref.Host) || strings.Contains(ref.Host, ":") {
			t.Fatalf("bad host %q", ref.Host)
		}
		if strings.HasPrefix(string(ref.Project), "/") || strings.HasSuffix(string(ref.Project), "/") ||
			!strings.Contains(string(ref.Project), "/") {
			t.Fatalf("bad project %q", ref.Project)
		}
		for _, seg := range strings.Split(string(ref.Project), "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("bad project %q", ref.Project)
			}
		}
		if ref.InstanceID().Validate() != nil {
			t.Fatalf("bad instance id %q", ref.InstanceID())
		}
		// round trip
		again, err := ParseRemote(ref.Format())
		ref.HadCredentials = false
		if err != nil || again != ref {
			t.Fatalf("round trip of %+v = %+v, %v", ref, again, err)
		}
	})
}
