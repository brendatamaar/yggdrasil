package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitOut runs the real git on a generated repo, read-only. It skips the test
// when git is not installed. The generator itself never starts a process;
// this only checks that real git understands what it wrote.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	exe, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	cmd := exec.Command(exe, append([]string{"--no-optional-locks", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// TestGenerate_ExtraRepos checks the states real git reports for the repos
// that cover the cases the first three do not.
func TestGenerate_ExtraRepos(t *testing.T) {
	root, _ := genDemo(t, filepath.Join(t.TempDir(), "with space"))
	proj := filepath.Join(root, "acme-shop")

	t.Run("GatewayIsCleanAndInSync", func(t *testing.T) {
		out := gitOut(t, filepath.Join(proj, "shop-gateway"), "status", "--porcelain=v2", "--branch")
		if !strings.Contains(out, "# branch.head main") || !strings.Contains(out, "# branch.ab +0 -0") {
			t.Errorf("want main, +0 -0:\n%s", out)
		}
		if body := strings.ReplaceAll(out, "\r", ""); strings.Count(body, "\n") != strings.Count(body, "# ") {
			t.Errorf("working tree is not clean:\n%s", out)
		}
	})

	t.Run("PaymentsHeadIsDetached", func(t *testing.T) {
		dir := filepath.Join(proj, "shop-payments")
		out := gitOut(t, dir, "status", "--porcelain=v2", "--branch")
		if !strings.Contains(out, "# branch.head (detached)") {
			t.Errorf("HEAD is not detached:\n%s", out)
		}
		head := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
		tag := strings.TrimSpace(gitOut(t, dir, "rev-parse", "v0.1.0^{commit}"))
		if head != tag {
			t.Errorf("HEAD %s is not v0.1.0 %s", head, tag)
		}
	})

	t.Run("InventoryHasAConflict", func(t *testing.T) {
		dir := filepath.Join(proj, "shop-inventory")
		out := gitOut(t, dir, "status", "--porcelain=v2", "--branch")
		if !strings.Contains(out, "u UU ") || !strings.Contains(out, "src/inventory/stock.py") {
			t.Errorf("no unmerged src/inventory/stock.py:\n%s", out)
		}
		if !strings.Contains(out, "# branch.ab +1 -0") {
			t.Errorf("want one commit ahead of origin/main:\n%s", out)
		}
		if got := strings.TrimSpace(gitOut(t, dir, "diff", "--name-only", "--diff-filter=U")); got != "src/inventory/stock.py" {
			t.Errorf("unmerged files = %q", got)
		}
		stock, err := os.ReadFile(filepath.Join(dir, "src", "inventory", "stock.py"))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range []string{"<<<<<<< HEAD", "=======", ">>>>>>> feature/low-stock-alert"} {
			if !strings.Contains(string(stock), m) {
				t.Errorf("stock.py lacks %q:\n%s", m, stock)
			}
		}
	})

	t.Run("ExistingRepoStatesSurvive", func(t *testing.T) {
		out := gitOut(t, filepath.Join(proj, "shop-web"), "status", "--porcelain=v2", "--branch")
		if !strings.Contains(out, "# branch.ab +0 -2") || !strings.Contains(out, "src/debug.ts") {
			t.Errorf("shop-web should be 2 behind and dirty:\n%s", out)
		}
	})
}

// TestGenerate_ToolchainManifests: the files toolchain detection reads exist.
func TestGenerate_ToolchainManifests(t *testing.T) {
	root, _ := genDemo(t, filepath.Join(t.TempDir(), "out"))
	proj := filepath.Join(root, "acme-shop")
	want := map[string][]string{
		"shop-api":       {"pom.xml", "mvnw", "mvnw.cmd"},
		"shop-web":       {"package.json", "pnpm-lock.yaml"},
		"shop-admin":     {"package.json"},
		"shop-gateway":   {"settings.gradle.kts", "build.gradle.kts", "gradlew", "gradlew.bat", "gradle.lockfile", "gradle/wrapper/gradle-wrapper.properties"},
		"shop-payments":  {"go.mod"},
		"shop-inventory": {"pyproject.toml", "uv.lock"},
	}
	for repo, files := range want {
		for _, name := range files {
			if _, err := os.Stat(filepath.Join(proj, repo, filepath.FromSlash(name))); err != nil {
				t.Errorf("%s: %v", repo, err)
			}
		}
	}
	// The Windows wrappers keep CRLF in the committed blob.
	bat, err := os.ReadFile(filepath.Join(proj, "shop-gateway", "gradlew.bat"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bat), "\r\n") || strings.Contains(strings.ReplaceAll(string(bat), "\r\n", ""), "\n") {
		t.Errorf("gradlew.bat must use CRLF only: %q", bat)
	}
	// Exactly the repos in the table are generated, and each has a bare origin.
	for _, spec := range repos {
		if _, ok := want[spec.name]; !ok {
			t.Errorf("repo %s has no manifest expectations", spec.name)
		}
		if _, err := os.Stat(filepath.Join(root, "remotes", spec.name+".git", "HEAD")); err != nil {
			t.Errorf("%s: no bare origin: %v", spec.name, err)
		}
	}
}

// TestGenerate_HostFixtures: the recorded host answers are valid JSON, have
// the fields the host clients read, and name only fictional hosts.
func TestGenerate_HostFixtures(t *testing.T) {
	root, _ := genDemo(t, filepath.Join(t.TempDir(), "out"))
	hosts := filepath.Join(root, "hosts")

	required := map[string][]string{
		"gitlab/mrs.json":       {"iid", "title", "state", "source_branch", "target_branch", "web_url"},
		"gitlab/pipelines.json": {"id", "status", "ref", "web_url"},
		"github/pulls.json":     {"number", "title", "state", "html_url", "head", "base"},
		"gitea/pulls.json":      {"number", "title", "state", "html_url", "head", "base"},
		"gitea/status.json":     nil,
		"github/checkruns.json": nil,
	}
	for name, fields := range required {
		raw, err := os.ReadFile(filepath.Join(hosts, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Errorf("%s: invalid JSON: %v", name, err)
			continue
		}
		list, isList := v.([]any)
		if !isList {
			continue
		}
		if len(list) == 0 {
			t.Errorf("%s: empty list", name)
		}
		for i, item := range list {
			obj, _ := item.(map[string]any)
			for _, fld := range fields {
				if _, ok := obj[fld]; !ok {
					t.Errorf("%s[%d] lacks %q", name, i, fld)
				}
			}
		}
	}

	// A failed and a successful pipeline, and a draft and a merged request, so
	// a widget has every row kind to show.
	pipes := mustRead(t, filepath.Join(hosts, "gitlab", "pipelines.json"))
	for _, s := range []string{`"failed"`, `"success"`, `"running"`} {
		if !strings.Contains(pipes, s) {
			t.Errorf("gitlab pipelines lack status %s", s)
		}
	}

	err := filepath.WalkDir(hosts, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body := mustRead(t, p)
		for _, bad := range []string{"gitlab.com", "github.com", "gitea.com", "token", "Authorization", "C:\\", "/home/", "/Users/"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s mentions %q: fixtures must be fictional", p, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
