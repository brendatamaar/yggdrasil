package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
)

// The three repos below cover the repo states and toolchains the first three
// do not: a Gradle repo that is clean and in sync, a Go repo with a detached
// HEAD, and a uv Python repo in the middle of a conflicted merge.

// checkoutDetached checks out the commit a lightweight tag points at and
// leaves HEAD detached there.
func (b *builder) checkoutDetached(tagName string) {
	b.flush()
	if b.err != nil {
		return
	}
	h, err := ref(b.localRefs, plumbing.NewTagReferenceName(tagName))
	if err != nil {
		b.fail(err, "detach at %s", tagName)
		return
	}
	wt, err := b.local.Worktree()
	if err != nil {
		b.fail(err, "open worktree")
		return
	}
	b.fail(wt.Checkout(&git.CheckoutOptions{Hash: h}), "check out %s", tagName)
}

// markConflict turns the checked-out branch into a merge of theirsBranch that
// stopped on a conflict in path: the index holds the three stages, the file
// holds conflict markers and MERGE_HEAD names the other side.
func (b *builder) markConflict(path, base, ours, theirs, theirsBranch string) {
	if b.err != nil {
		return
	}
	theirsHead, err := ref(b.localRefs, plumbing.NewBranchReferenceName(theirsBranch))
	if err != nil {
		b.fail(err, "mark conflict")
		return
	}
	stage := func(content string) plumbing.Hash {
		obj := b.local.Storer.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, err := obj.Writer()
		if err == nil {
			_, err = w.Write([]byte(content))
			if cerr := w.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			b.fail(err, "write blob for %s", path)
			return plumbing.ZeroHash
		}
		h, err := b.local.Storer.SetEncodedObject(obj)
		b.fail(err, "store blob for %s", path)
		return h
	}
	hashes := [3]plumbing.Hash{stage(base), stage(ours), stage(theirs)}
	if b.err != nil {
		return
	}
	idx, err := b.local.Storer.Index()
	if err != nil {
		b.fail(err, "read index")
		return
	}
	kept := idx.Entries[:0]
	for _, e := range idx.Entries {
		if e.Name != path {
			kept = append(kept, e)
		}
	}
	idx.Entries = kept
	for i, st := range []index.Stage{index.AncestorMode, index.OurMode, index.TheirMode} {
		idx.Entries = append(idx.Entries, &index.Entry{Name: path, Hash: hashes[i], Mode: filemode.Regular, Stage: st})
	}
	sort.SliceStable(idx.Entries, func(i, j int) bool {
		a, c := idx.Entries[i], idx.Entries[j]
		if a.Name != c.Name {
			return a.Name < c.Name
		}
		return a.Stage < c.Stage
	})
	b.fail(b.local.Storer.SetIndex(idx), "write index")

	markers := "<<<<<<< HEAD\n" + ours + "=======\n" + theirs + ">>>>>>> " + theirsBranch + "\n"
	b.writeFile(path, markers)
	gitDir := filepath.Join(b.dir, git.GitDirName)
	b.fail(os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(theirsHead.String()+"\n"), 0o644), "write MERGE_HEAD")
	b.fail(os.WriteFile(filepath.Join(gitDir, "MERGE_MODE"), nil, 0o644), "write MERGE_MODE")
	b.fail(os.WriteFile(filepath.Join(gitDir, "MERGE_MSG"), []byte("Merge branch '"+theirsBranch+"'\n\nConflicts:\n\t"+path+"\n"), 0o644), "write MERGE_MSG")
	b.note("conflicted: merge of " + theirsBranch + " stopped on " + path)
}

// buildShopGateway: Gradle (Kotlin DSL) edge service. Clean and in sync with
// origin, a wrapper with a CRLF gradlew.bat and a dependency lock file.
func buildShopGateway(b *builder) {
	b.commit("main", bram, "chore: initial Gradle project",
		f("settings.gradle.kts", "rootProject.name = \"shop-gateway\"\n"),
		f("build.gradle.kts", gatewayBuild(false)),
		f("gradle.properties", "org.gradle.jvmargs=-Xmx512m\norg.gradle.caching=true\n"),
		f("gradlew", gradlew),
		f("gradlew.bat", strings.ReplaceAll(gradlewBat, "\n", "\r\n")),
		f("gradle/wrapper/gradle-wrapper.properties", "distributionBase=GRADLE_USER_HOME\ndistributionPath=wrapper/dists\ndistributionUrl=https\\://services.gradle.org/distributions/gradle-8.10-bin.zip\nzipStoreBase=GRADLE_USER_HOME\nzipStorePath=wrapper/dists\n"),
		f(".gitignore", "build/\n.gradle/\n.idea/\n"),
		f("README.md", "# shop-gateway\n\nEdge routing for acme-shop.\n\n    ./gradlew bootRun\n"),
		f("src/main/kotlin/com/example/acme/gateway/Application.kt", "package com.example.acme.gateway\n\nfun main() {\n    println(\"shop-gateway\")\n}\n"))
	b.commit("main", chen, "feat(routes): add route table",
		f("src/main/kotlin/com/example/acme/gateway/Routes.kt", "package com.example.acme.gateway\n\nval routes = mapOf(\n    \"/api\" to \"http://localhost:8081\",\n    \"/admin\" to \"http://localhost:8082\",\n)\n"))
	b.commit("main", ada, "build: lock dependencies",
		f("gradle.lockfile", "# This is a Gradle generated file for dependency locking.\norg.jetbrains.kotlin:kotlin-stdlib:2.0.20=compileClasspath,runtimeClasspath\nempty=\n"),
		f("build.gradle.kts", gatewayBuild(true)))
	b.commit("main", dana, "test(routes): cover route lookup",
		f("src/test/kotlin/com/example/acme/gateway/RoutesTest.kt", "package com.example.acme.gateway\n\nimport kotlin.test.Test\nimport kotlin.test.assertTrue\n\nclass RoutesTest {\n    @Test\n    fun hasApiRoute() = assertTrue(routes.containsKey(\"/api\"))\n}\n"))
	b.tag("v0.1.0", "main", author{}, "")
	b.push("main")
	b.pushTag("v0.1.0")
	b.checkout("main")
	b.note("clean and in sync with origin/main")
}

// buildShopPayments: Go service whose HEAD is detached at the v0.1.0 tag.
func buildShopPayments(b *builder) {
	b.commit("main", chen, "chore: initial Go module",
		f("go.mod", "module example.com/acme/shop-payments\n\ngo 1.22\n"),
		f("go.sum", ""),
		f("main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"shop-payments\")\n}\n"),
		f(".gitignore", "bin/\n"),
		f("README.md", "# shop-payments\n\nPayment intents for acme-shop.\n"))
	b.commit("main", dana, "feat(intent): add payment intent type",
		f("intent.go", "package main\n\ntype Intent struct {\n\tID     string\n\tAmount int64\n}\n"))
	b.tag("v0.1.0", "main", author{}, "")
	b.commit("main", chen, "feat(intent): validate amount",
		f("intent.go", "package main\n\nimport \"errors\"\n\ntype Intent struct {\n\tID     string\n\tAmount int64\n}\n\nfunc (i Intent) Validate() error {\n\tif i.Amount <= 0 {\n\t\treturn errors.New(\"amount must be positive\")\n\t}\n\treturn nil\n}\n"))
	b.commit("main", ada, "test(intent): cover validation",
		f("intent_test.go", "package main\n\nimport \"testing\"\n\nfunc TestValidate(t *testing.T) {\n\tif (Intent{}).Validate() == nil {\n\t\tt.Fatal(\"zero amount must be rejected\")\n\t}\n}\n"))
	b.push("main")
	b.pushTag("v0.1.0")
	b.checkoutDetached("v0.1.0")
	b.note("detached HEAD at v0.1.0")
}

// buildShopInventory: Python (uv) service in the middle of a conflicted merge,
// and one commit ahead of origin/main.
func buildShopInventory(b *builder) {
	const stockBase = "LOW_STOCK_THRESHOLD = 5\n"
	const stockOurs = "LOW_STOCK_THRESHOLD = 8\n"
	const stockTheirs = "LOW_STOCK_THRESHOLD = 3\n"
	const stockPath = "src/inventory/stock.py"

	b.commit("main", dana, "chore: initial uv project",
		f("pyproject.toml", "[project]\nname = \"shop-inventory\"\nversion = \"0.1.0\"\nrequires-python = \">=3.11\"\ndependencies = [\"fastapi>=0.110\"]\n\n[dependency-groups]\ndev = [\"pytest>=8\"]\n\n[tool.pytest.ini_options]\ntestpaths = [\"tests\"]\n"),
		f("uv.lock", "version = 1\nrequires-python = \">=3.11\"\n\n[[package]]\nname = \"shop-inventory\"\nversion = \"0.1.0\"\nsource = { editable = \".\" }\n"),
		f(".python-version", "3.12\n"),
		f(".gitignore", ".venv/\n__pycache__/\n"),
		f("README.md", "# shop-inventory\n\nStock levels for acme-shop.\n\n    uv sync\n    uv run pytest\n"),
		f("src/inventory/__init__.py", ""),
		f(stockPath, stockBase),
		f("tests/test_stock.py", "from inventory.stock import LOW_STOCK_THRESHOLD\n\n\ndef test_threshold_is_positive():\n    assert LOW_STOCK_THRESHOLD > 0\n"))
	b.commit("main", bram, "feat(stock): add reservation helper",
		f("src/inventory/reserve.py", "def reserve(available: int, wanted: int) -> int:\n    return min(available, wanted)\n"))
	b.push("main")

	b.branch("feature/low-stock-alert", "main")
	b.commit("feature/low-stock-alert", ada, "feat(stock): lower the low-stock threshold",
		f(stockPath, stockTheirs))
	b.commit("main", chen, "feat(stock): raise the low-stock threshold",
		f(stockPath, stockOurs))
	b.checkout("main")
	b.markConflict(stockPath, stockBase, stockOurs, stockTheirs, "feature/low-stock-alert")
}

func gatewayBuild(lock bool) string {
	s := "plugins {\n    kotlin(\"jvm\") version \"2.0.20\"\n    application\n}\n\nrepositories {\n    mavenCentral()\n}\n\ndependencies {\n    testImplementation(kotlin(\"test\"))\n}\n\napplication {\n    mainClass.set(\"com.example.acme.gateway.ApplicationKt\")\n}\n"
	if lock {
		s += "\ndependencyLocking {\n    lockAllConfigurations()\n}\n"
	}
	return s
}

const gradlew = `#!/bin/sh
# Gradle wrapper stub for the acme-shop demo. It does not download Gradle.
exec gradle "$@"
`

const gradlewBat = `@REM Gradle wrapper stub for the acme-shop demo. It does not download Gradle.
@echo off
gradle %*
`
