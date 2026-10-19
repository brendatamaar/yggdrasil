package hosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

func parse(t *testing.T, text string) Config {
	t.Helper()
	vars, probs := ParseEnvText(text)
	cfg := ParseConfig(vars)
	cfg.Problems = append(probs, cfg.Problems...)
	return cfg
}

func TestParseConfigDocumentedExample(t *testing.T) {
	cfg := parse(t, `# comment
YGG_HOST_1=gitlab https://git.acme.dev
YGG_HOST_1_TOKEN=glpat-secret-1            # read_api scope is enough
YGG_HOST_2=github https://github.com
YGG_HOST_2_TOKEN=
YGG_HOST_3 = gitea https://Code.Example.org:8443/gitea/
`)
	if len(cfg.Problems) != 0 {
		t.Fatalf("problems: %v", cfg.Problems)
	}
	want := []InstanceConfig{
		{ID: "git.acme.dev", Kind: corehosts.KindGitLab, Base: "https://git.acme.dev"},
		{ID: "github.com", Kind: corehosts.KindGitHub, Base: "https://github.com"},
		{ID: "code.example.org:8443", Kind: corehosts.KindGitea, Base: "https://code.example.org:8443/gitea"},
		{ID: "gitlab.com", Kind: corehosts.KindGitLab, Base: "https://gitlab.com"},
	}
	if len(cfg.Instances) != len(want) {
		t.Fatalf("instances = %+v", cfg.Instances)
	}
	for i := range want {
		if cfg.Instances[i] != want[i] {
			t.Errorf("instance %d = %+v, want %+v", i, cfg.Instances[i], want[i])
		}
	}
	if cfg.Token("git.acme.dev") != "glpat-secret-1" || cfg.Token("github.com") != "" {
		t.Errorf("tokens = %v", cfg.tokens)
	}
	if s := cfg.Secrets(); len(s) != 1 || s[0] != "glpat-secret-1" {
		t.Errorf("secrets = %v", s)
	}
	for _, in := range cfg.CoreInstances() {
		if in.ID == "git.acme.dev" != in.Token {
			t.Errorf("core instance %+v has the wrong token flag", in)
		}
	}
}

func TestGitHubAndGitLabKnownWithoutConfig(t *testing.T) {
	cfg := parse(t, "")
	if len(cfg.Instances) != 2 || cfg.Instances[0].ID != "github.com" || cfg.Instances[1].ID != "gitlab.com" || len(cfg.Secrets()) != 0 {
		t.Fatalf("defaults = %+v", cfg.Instances)
	}
}

func TestParseConfigRejectsEachRule(t *testing.T) {
	cases := map[string]string{
		"unknown kind":     "YGG_HOST_1=bitbucket https://b.acme.dev",
		"no scheme":        "YGG_HOST_1=gitlab git.acme.dev",
		"http non-local":   "YGG_HOST_1=gitlab http://git.acme.dev",
		"ftp":              "YGG_HOST_1=gitlab ftp://git.acme.dev",
		"credentials":      "YGG_HOST_1=gitlab https://u:p@git.acme.dev",
		"query":            "YGG_HOST_1=gitlab https://git.acme.dev?x=1",
		"one field":        "YGG_HOST_1=gitlab",
		"three fields":     "YGG_HOST_1=gitlab https://a.dev extra",
		"bad port":         "YGG_HOST_1=gitlab https://git.acme.dev:0",
		"token without it": "YGG_HOST_5_TOKEN=abc",
		"duplicate":        "YGG_HOST_1=gitlab https://git.acme.dev\nYGG_HOST_2=gitea https://GIT.acme.dev",
		"malformed line":   "this is not an assignment",
		"token with space": "YGG_HOST_1=gitlab https://git.acme.dev\nYGG_HOST_1_TOKEN=abc def",
	}
	for name, text := range cases {
		cfg := parse(t, text)
		if len(cfg.Problems) == 0 {
			t.Errorf("%s: no problem reported (%+v)", name, cfg.Instances)
		}
		for _, p := range cfg.Problems {
			for _, secret := range []string{"abc", "SECRET"} {
				if strings.Contains(p.String(), secret) && secret == "SECRET" {
					t.Errorf("%s: problem echoes a value: %s", name, p)
				}
			}
		}
	}
	// localhost over http is fine.
	if cfg := parse(t, "YGG_HOST_1=gitea http://localhost:3000"); len(cfg.Problems) != 0 || cfg.Instances[0].ID != "localhost:3000" {
		t.Errorf("localhost = %+v %v", cfg.Instances, cfg.Problems)
	}
}

func TestProblemsNameLinesNotValues(t *testing.T) {
	cfg := parse(t, "\nYGG_HOST_1=bitbucket https://SECRET-host.dev\nYGG_HOST_1_TOKEN=SECRET-token\n")
	if len(cfg.Problems) != 1 || cfg.Problems[0].Line != 2 || !strings.Contains(cfg.Problems[0].String(), "line 2") {
		t.Fatalf("problems = %v", cfg.Problems)
	}
	if strings.Contains(cfg.Problems[0].String(), "SECRET") {
		t.Fatal("a problem echoes a value")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	cfg := parse(t, "YGG_HOST_1=gitlab https://git.acme.dev/gl\nYGG_HOST_1_TOKEN=tok1\nYGG_HOST_2=gitea http://localhost:3000\n")
	text := FormatConfig(cfg, true)
	again := parse(t, text)
	if len(again.Problems) != 0 || len(again.Instances) != len(cfg.Instances) {
		t.Fatalf("round trip: %v\n%s", again.Problems, text)
	}
	for i := range cfg.Instances {
		if again.Instances[i] != cfg.Instances[i] || again.Token(cfg.Instances[i].ID) != cfg.Token(cfg.Instances[i].ID) {
			t.Errorf("instance %d changed: %+v -> %+v", i, cfg.Instances[i], again.Instances[i])
		}
	}
	if strings.Contains(FormatConfig(cfg, false), "tok1") {
		t.Error("FormatConfig(false) wrote a token")
	}
}

func TestEnvPath(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	if got := EnvPath("data", none); got != filepath.Join("data", ".env") {
		t.Errorf("default = %q", got)
	}
	set := func(string) (string, bool) { return filepath.Join("x", "my.env"), true }
	if got := EnvPath("data", set); got != filepath.Join("x", "my.env") {
		t.Errorf("YGG_ENV_FILE = %q", got)
	}
}

func TestLoadConfigFileAndProcessVariables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("YGG_HOST_1=gitlab https://git.acme.dev\r\nYGG_HOST_1_TOKEN=from-file\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"YGG_HOST_1_TOKEN": "from-env", "YGG_HOST_2": "gitea https://code.example.org"}
	cfg, err := LoadConfig(path, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token("git.acme.dev") != "from-env" || len(cfg.Instances) != 4 {
		t.Fatalf("cfg = %+v tokens %v", cfg.Instances, cfg.tokens)
	}
	// A missing file is fine.
	cfg, err = LoadConfig(filepath.Join(dir, "none.env"), func(string) (string, bool) { return "", false })
	if err != nil || len(cfg.Instances) != 2 {
		t.Fatalf("missing file: %+v, %v", cfg.Instances, err)
	}
}

func FuzzParseConfig(f *testing.F) {
	f.Add("YGG_HOST_1=gitlab https://git.acme.dev\nYGG_HOST_1_TOKEN=x\n")
	f.Add("\xef\xbb\xbfYGG_HOST_9999=gitea http://localhost")
	f.Fuzz(func(t *testing.T, text string) {
		vars, probs := ParseEnvText(text)
		cfg := ParseConfig(vars)
		seen := map[corehosts.InstanceID]bool{}
		for _, in := range cfg.Instances {
			if in.ID.Validate() != nil || in.Kind.Validate() != nil || seen[in.ID] || !strings.HasPrefix(in.Base, "http") {
				t.Fatalf("invalid instance %+v", in)
			}
			seen[in.ID] = true
		}
		for _, p := range append(probs, cfg.Problems...) {
			for _, tok := range cfg.Secrets() {
				if len(tok) > 3 && strings.Contains(p.String(), tok) {
					t.Fatalf("problem echoes a token: %s", p)
				}
			}
		}
		// round trip
		again := ParseConfig(mustVars(FormatConfig(cfg, true)))
		if len(again.Instances) != len(cfg.Instances) {
			t.Fatalf("round trip lost instances: %v vs %v", cfg.Instances, again.Instances)
		}
	})
}

func mustVars(text string) map[string]Var {
	v, _ := ParseEnvText(text)
	return v
}
