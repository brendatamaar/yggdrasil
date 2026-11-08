package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// scenarioMultiRepo names the multi-repo scenario (PRD RV-03, RV-04): one
// task that touches repos which depend on each other, shipped as linked merge
// requests. It is a separate project, so the six-repo demo project and the
// tests pinned to it do not change.
const scenarioMultiRepo = "multirepo"

const scenarioReadme = `# acme-shop-platform (demo scenario)

A fictional project for tests of multi-repo change sets. Every name is made up.

- shop-core: Java / Maven library (com.example.acme:shop-core)
- shop-checkout: Java / Maven service that depends on shop-core, so the Maven
  order puts shop-core first
- shop-storefront: TypeScript storefront (pnpm), independent of the other two;
  docs/RELEASE-NOTES.txt is committed with CRLF line endings

The origin of each repo is a local bare repo under ../remotes. Nothing here
talks to a real git host.
`

var scenarioRepos = []repoSpec{
	{"shop-core", time.Date(2026, 5, 4, 8, 0, 0, 0, time.UTC), buildShopCore},
	{"shop-checkout", time.Date(2026, 5, 11, 8, 0, 0, 0, time.UTC), buildShopCheckout},
	{"shop-storefront", time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC), buildShopStorefront},
}

// generateScenario writes the multi-repo scenario into out (named
// demo-project, empty or missing) and returns its absolute path. It never
// deletes anything.
func generateScenario(name, out string) (string, []repoSummary, error) {
	if name != scenarioMultiRepo {
		return "", nil, fmt.Errorf("unknown scenario %q (known: %s)", name, scenarioMultiRepo)
	}
	root, err := prepareOut(out)
	if err != nil {
		return "", nil, err
	}
	project := filepath.Join(root, "acme-shop-platform")
	if err := os.MkdirAll(project, 0o755); err != nil {
		return "", nil, fmt.Errorf("create %s: %w", project, err)
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte(scenarioReadme), 0o644); err != nil {
		return "", nil, fmt.Errorf("write scenario README: %w", err)
	}
	sums := make([]repoSummary, 0, len(scenarioRepos))
	for _, spec := range scenarioRepos {
		s, err := buildRepo(spec, filepath.Join(project, spec.name), filepath.Join(root, "remotes", spec.name+".git"))
		if err != nil {
			return "", nil, err
		}
		sums = append(sums, s)
	}
	return root, sums, nil
}

func scenarioPom(artifact string, deps ...string) string {
	var d string
	if len(deps) > 0 {
		d = "    <dependencies>\n"
		for _, dep := range deps {
			d += "        <dependency>\n            <groupId>com.example.acme</groupId>\n            <artifactId>" + dep + "</artifactId>\n            <version>1.0.0-SNAPSHOT</version>\n        </dependency>\n"
		}
		d += "    </dependencies>\n"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
    <modelVersion>4.0.0</modelVersion>
    <groupId>com.example.acme</groupId>
    <artifactId>` + artifact + `</artifactId>
    <version>1.0.0-SNAPSHOT</version>
    <properties>
        <java.version>21</java.version>
    </properties>
` + d + `</project>
`
}

func buildShopCore(b *builder) {
	b.commit("main", ada, "chore: initial Maven library",
		f("pom.xml", scenarioPom("shop-core")),
		f("mvnw", mvnw),
		f("mvnw.cmd", strings.ReplaceAll(mvnwCmd, "\n", "\r\n")),
		f(".gitignore", "target/\n"),
		f("README.md", "# shop-core\n\nShared money and pricing types for acme-shop.\n"),
		f("src/main/java/com/example/acme/core/Money.java", "package com.example.acme.core;\n\npublic record Money(long cents, String currency) {}\n"))
	b.commit("main", bram, "feat(money): add currency check",
		f("src/main/java/com/example/acme/core/Currency.java", "package com.example.acme.core;\n\npublic final class Currency {\n    private Currency() {}\n\n    public static boolean isKnown(String code) {\n        return code != null && code.length() == 3;\n    }\n}\n"))
	b.push("main")
	b.checkout("main")
	b.note("Maven library, no dependencies on other repos")
}

func buildShopCheckout(b *builder) {
	b.commit("main", chen, "chore: initial Maven service",
		f("pom.xml", scenarioPom("shop-checkout", "shop-core")),
		f("mvnw", mvnw),
		f("mvnw.cmd", strings.ReplaceAll(mvnwCmd, "\n", "\r\n")),
		f(".gitignore", "target/\n"),
		f("README.md", "# shop-checkout\n\nCheckout service for acme-shop. Uses shop-core.\n"),
		f("src/main/java/com/example/acme/checkout/Checkout.java", "package com.example.acme.checkout;\n\nimport com.example.acme.core.Money;\n\npublic class Checkout {\n    public Money total() {\n        return new Money(0, \"EUR\");\n    }\n}\n"))
	b.commit("main", dana, "feat(checkout): add order summary",
		f("src/main/java/com/example/acme/checkout/Summary.java", "package com.example.acme.checkout;\n\npublic record Summary(String orderId, int lines) {}\n"))
	b.push("main")
	b.checkout("main")
	b.note("Maven service that depends on shop-core")
}

func buildShopStorefront(b *builder) {
	b.commit("main", bram, "chore: initial storefront",
		f("package.json", "{\n  \"name\": \"shop-storefront\",\n  \"version\": \"0.1.0\",\n  \"private\": true,\n  \"type\": \"module\",\n  \"scripts\": {\n    \"dev\": \"vite\",\n    \"build\": \"tsc && vite build\",\n    \"lint\": \"eslint .\",\n    \"test\": \"vitest run\"\n  }\n}\n"),
		f("pnpm-lock.yaml", "lockfileVersion: '9.0'\n\nsettings:\n  autoInstallPeers: true\n  excludeLinksFromLockfile: false\n\nimporters:\n\n  .: {}\n"),
		f(".gitignore", "node_modules/\ndist/\n"),
		f("README.md", "# shop-storefront\n\nStorefront for acme-shop.\n"),
		f("src/main.ts", "export const app = 'shop-storefront';\n"),
		f("docs/RELEASE-NOTES.txt", strings.ReplaceAll("Release notes\n\n0.1.0\n- first storefront build\n", "\n", "\r\n")))
	b.commit("main", ada, "feat(cart): show cart count",
		f("src/cart.ts", "export function cartCount(lines: number[]): number {\n  return lines.reduce((n, l) => n + l, 0);\n}\n"))
	b.push("main")
	b.checkout("main")
	b.note("pnpm storefront, docs/RELEASE-NOTES.txt has CRLF line endings")
}
