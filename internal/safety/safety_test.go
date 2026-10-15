package safety

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestEnvBuilderDropsParentSecrets(t *testing.T) {
	parent := []string{
		"PATH=C:\\bin", "SystemRoot=C:\\Windows", "ProgramFiles(x86)=C:\\pf86", "Temp=C:\\tmp", "=C:=C:\\",
		"GITHUB_TOKEN=ghp_abcdefghijklmnop", "GITLAB_TOKEN=glpat-abcdefghijklmnop", "ANTHROPIC_API_KEY=sk-ant-abcdefghij",
		"VIRTUAL_ENV=C:\\venv", "CONDA_PREFIX=C:\\conda", "PYTHONPATH=C:\\py", "RANDOM_VAR=x",
	}
	env, err := envBuilder{environ: func() []string { return parent }}.Build(EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=C:\\bin", "ProgramFiles(x86)=C:\\pf86", "SystemRoot=C:\\Windows", "Temp=C:\\tmp"}
	if !slices.Equal(env, want) {
		t.Fatalf("env = %q, want %q", env, want)
	}
}

func TestEnvBuilderRealEnvironment(t *testing.T) {
	for _, k := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "ANTHROPIC_API_KEY", "VIRTUAL_ENV", "PYTHONPATH", "CONDA_PREFIX"} {
		t.Setenv(k, "leak-me")
	}
	env, err := NewEnvBuilder().Build(EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if env == nil {
		t.Fatal("env is nil")
	}
	for _, kv := range env {
		if strings.Contains(kv, "leak-me") {
			t.Fatalf("env leaks %q", kv)
		}
	}
}

func TestEnvBuilderNeverNil(t *testing.T) {
	env, err := envBuilder{environ: func() []string { return nil }}.Build(EnvOptions{})
	if err != nil || env == nil || len(env) != 0 {
		t.Fatalf("env = %#v, %v", env, err)
	}
}

func TestEnvBuilderExtra(t *testing.T) {
	b := envBuilder{environ: func() []string { return []string{"Path=C:\\bin", "LANG=en"} }}
	env, err := b.Build(EnvOptions{Extra: map[string]string{"PATH": "D:\\x", "YGG_TASK": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"LANG=en", "PATH=D:\\x", "YGG_TASK=1"}; !slices.Equal(env, want) {
		t.Fatalf("env = %q, want %q", env, want)
	}
	for _, name := range []string{"GITHUB_TOKEN", "my_secret", "DB_PASSWORD", "ANTHROPIC_API_KEY", "SSH_KEY", "aws-credentials"} {
		if _, err := b.Build(EnvOptions{Extra: map[string]string{name: "v"}}); err == nil {
			t.Errorf("extra %s accepted", name)
		}
	}
	env, err = b.Build(EnvOptions{Extra: map[string]string{"proxy_token": "v"}, Allow: []string{"PROXY_TOKEN"}})
	if err != nil || !slices.Contains(env, "proxy_token=v") {
		t.Fatalf("allowed extra: %q, %v", env, err)
	}
	for _, bad := range []string{"", "A=B", "A\x00"} {
		if _, err := b.Build(EnvOptions{Extra: map[string]string{bad: "v"}}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func TestRedact(t *testing.T) {
	r := NewRedactor("hunter2-the-secret", "")
	cases := []struct{ name, in, want string }{
		{"bearer", "Authorization: Bearer abc.DEF-123_x", "Authorization: Bearer " + Redacted},
		{"bearer lowercase", "authorization: bearer abc123", "authorization: bearer " + Redacted},
		{"basic", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "Authorization: Basic " + Redacted},
		{"basic prose", "Basic use", "Basic use"},
		{"url creds", "clone https://user:s3cr3t@github.com/a/b.git now", "clone https://" + Redacted + "@github.com/a/b.git now"},
		{"url no creds", "https://github.com/a/b.git ssh://git@host/x", "https://github.com/a/b.git ssh://git@host/x"},
		{"ghp", "token ghp_abcdefghijklmnopqrstuvwxyz0123456789 end", "token " + Redacted + " end"},
		{"github_pat", "github_pat_11ABCDEFG0abcdefghij_klmnop", Redacted},
		{"glpat", "x glpat-abcdefghij1234567890 y", "x " + Redacted + " y"},
		{"sk-ant", "key sk-ant-api03-abcdefghij", "key " + Redacted},
		{"token pair", "token=abc123&x=1", "token=" + Redacted + "&x=1"},
		{"password pair", "password: hunter2 next", "password: " + Redacted + " next"},
		{"api_key pair", "export API_KEY=zzz", "export API_KEY=" + Redacted},
		{"env style", "GITHUB_TOKEN=abc", "GITHUB_TOKEN=" + Redacted},
		{"json", `{"password":"p w","ok":"fine"}`, `{"password":"` + Redacted + `","ok":"fine"}`},
		{"configured secret", "value hunter2-the-secret here", "value " + Redacted + " here"},
		{"plain", "nothing to hide: tokenizer ready", "nothing to hide: tokenizer ready"},
		{"idempotent", "token=" + Redacted, "token=" + Redacted},
	}
	for _, c := range cases {
		if got := r.Redact(c.in); got != c.want {
			t.Errorf("%s: Redact(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestLogHandlerRedacts(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil), NewRedactor("topsecret")))
	log = log.With("url", "https://u:pw@host/x")
	log.Info("clone with Bearer abc123", "msg2", "token=zzz", "password", "plain",
		"err", errors.New("fail topsecret"), slog.Group("g", "v", "ghp_abcdefghijklmnopqrst"))
	out := buf.String()
	for _, leak := range []string{"abc123", "zzz", "plain", "topsecret", "u:pw", "ghp_"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, Redacted) {
		t.Errorf("no redaction marker: %s", out)
	}
}
