package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

var scenarioNames = []string{"shop-core", "shop-checkout", "shop-storefront"}

func genScenario(t *testing.T, dir string) string {
	t.Helper()
	root, sums, err := generateScenario(scenarioMultiRepo, filepath.Join(dir, outName))
	if err != nil {
		t.Fatalf("generateScenario: %v", err)
	}
	if len(sums) != len(scenarioRepos) {
		t.Fatalf("got %d summaries, want %d", len(sums), len(scenarioRepos))
	}
	return root
}

func scenarioHeads(t *testing.T, root string) map[string]plumbing.Hash {
	t.Helper()
	out := map[string]plumbing.Hash{}
	for _, n := range scenarioNames {
		for kind, p := range map[string]string{
			"local":  filepath.Join(root, "acme-shop-platform", n),
			"remote": filepath.Join(root, "remotes", n+".git"),
		} {
			r, err := git.PlainOpen(p)
			if err != nil {
				t.Fatalf("open %s: %v", p, err)
			}
			h, err := r.Head()
			if err != nil {
				t.Fatalf("%s %s HEAD: %v", n, kind, err)
			}
			out[n+" "+kind] = h.Hash()
		}
	}
	return out
}

// The scenario is generated once (under a path with a space) and again in
// another place: every head must match, and the content is what the Maven and
// Node toolchains and the order logic rely on.
func TestScenarioMultiRepo(t *testing.T) {
	tmp := t.TempDir()
	root := genScenario(t, filepath.Join(tmp, "with space"))
	other := genScenario(t, filepath.Join(tmp, "second"))

	t.Run("Deterministic", func(t *testing.T) {
		a, b := scenarioHeads(t, root), scenarioHeads(t, other)
		if len(a) != 2*len(scenarioNames) {
			t.Fatalf("heads = %v", a)
		}
		for k, h := range a {
			if h.IsZero() || b[k] != h {
				t.Errorf("%s: %s vs %s", k, h, b[k])
			}
		}
	})

	read := func(t *testing.T, repo, rel string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, "acme-shop-platform", repo, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	t.Run("MavenDependency", func(t *testing.T) {
		core, checkout := string(read(t, "shop-core", "pom.xml")), string(read(t, "shop-checkout", "pom.xml"))
		if strings.Contains(core, "<dependency>") {
			t.Error("shop-core must have no dependencies")
		}
		if !strings.Contains(checkout, "<groupId>com.example.acme</groupId>\n            <artifactId>shop-core</artifactId>") {
			t.Errorf("shop-checkout does not depend on shop-core:\n%s", checkout)
		}
	})

	t.Run("NodeRepo", func(t *testing.T) {
		if !bytes.Contains(read(t, "shop-storefront", "package.json"), []byte(`"build"`)) {
			t.Error("package.json has no build script")
		}
		read(t, "shop-storefront", "pnpm-lock.yaml")
	})

	t.Run("CRLF", func(t *testing.T) {
		for _, c := range []struct{ repo, rel string }{{"shop-storefront", "docs/RELEASE-NOTES.txt"}, {"shop-core", "mvnw.cmd"}} {
			b := read(t, c.repo, c.rel)
			if !bytes.Contains(b, []byte("\r\n")) || bytes.Contains(bytes.ReplaceAll(b, []byte("\r\n"), nil), []byte("\n")) {
				t.Errorf("%s/%s should use CRLF line endings only: %q", c.repo, c.rel, b)
			}
		}
	})

	t.Run("OriginsAreLocalAndFictional", func(t *testing.T) {
		for _, n := range scenarioNames {
			r, err := git.PlainOpen(filepath.Join(root, "acme-shop-platform", n))
			if err != nil {
				t.Fatal(err)
			}
			c, err := r.Config()
			if err != nil {
				t.Fatal(err)
			}
			urls := c.Remotes["origin"].URLs
			if len(urls) != 1 || strings.Contains(urls[0], "://") {
				t.Errorf("%s origin = %v, want a local path", n, urls)
			}
		}
		raw := string(read(t, "shop-core", "README.md")) + string(read(t, "shop-checkout", "pom.xml"))
		if strings.Contains(raw, "gitlab.com") || strings.Contains(raw, "github.com") {
			t.Error("scenario mentions a real host")
		}
	})
}

func TestScenarioRejectsUnknownNameAndBadOut(t *testing.T) {
	tmp := t.TempDir()
	if _, _, err := generateScenario("nope", filepath.Join(tmp, outName)); err == nil {
		t.Error("unknown scenario accepted")
	}
	if _, _, err := generateScenario(scenarioMultiRepo, filepath.Join(tmp, "wrong-name")); err == nil {
		t.Error("output folder not named demo-project accepted")
	}
}
