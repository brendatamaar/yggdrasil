package agentstream

import (
	"slices"
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestCleanEnvAllowlistOnly(t *testing.T) {
	ours := map[string]string{
		"GITHUB_TOKEN":          "fictional-gh",
		"GH_TOKEN":              "fictional-gh",
		"GITLAB_TOKEN":          "fictional-gl",
		"GITEA_TOKEN":           "fictional-gt",
		"ANTHROPIC_API_KEY":     "fictional-key",
		"AWS_SECRET_ACCESS_KEY": "fictional-aws",
		"GOPATH":                "C:/go",
	}
	for _, name := range BaseEnvAllowlist {
		ours[name] = "value-of-" + name
	}
	env, err := CleanEnv(func(k string) (string, bool) { v, ok := ours[k]; return v, ok },
		map[string]string{EnvMaxConcurrentSubagent: "4"})
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(env)
	for _, name := range BaseEnvAllowlist {
		if got[name] != "value-of-"+name {
			t.Errorf("%s = %q, want it passed through", name, got[name])
		}
	}
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "GITEA_TOKEN", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY", "GOPATH"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s reached the child env", name)
		}
	}
	if got[EnvMaxConcurrentSubagent] != "4" || len(got) != len(BaseEnvAllowlist)+1 {
		t.Errorf("env = %v", env)
	}
}

func TestCleanEnvRefusesSecretExtras(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "gitlab_token", "GITEA_TOKEN", "ANTHROPIC_API_KEY", "MY_SECRET", "DB_PASSWORD", "", "A=B"} {
		if _, err := CleanEnv(func(string) (string, bool) { return "", false }, map[string]string{name: "x"}); err == nil {
			t.Errorf("extra %q accepted", name)
		}
	}
}

func TestCleanEnvExtraReplacesAllowlisted(t *testing.T) {
	env, err := CleanEnv(func(k string) (string, bool) { return "ours", k == "PATH" }, map[string]string{"Path": "C:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(env, []string{"Path=C:/bin"}) {
		t.Errorf("env = %v", env)
	}
}

func TestCleanEnvNeverNil(t *testing.T) {
	env, err := CleanEnv(func(string) (string, bool) { return "", false }, nil)
	if err != nil || env == nil || len(env) != 0 {
		t.Fatalf("env = %#v, %v; want empty non-nil", env, err)
	}
}
