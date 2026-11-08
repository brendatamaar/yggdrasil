package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

var fxRepos = []string{"shop-ledger", "shop-api-dev", "shop-web-dev"}

// fxFiles lists the files of a generated working tree, slash separated.
func fxFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = mustRead(t, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFixtures_Deterministic: two generations give identical refs and trees
// for the Phase 5 repos and the uv repo's extra branch.
func TestFixtures_Deterministic(t *testing.T) {
	a, _ := genDemo(t, filepath.Join(t.TempDir(), "one"))
	b, _ := genDemo(t, filepath.Join(t.TempDir(), "two"))
	for _, name := range append([]string{"shop-inventory"}, fxRepos...) {
		ra, rb := allRefs(t, openLocal(t, a, name)), allRefs(t, openLocal(t, b, name))
		if len(ra) == 0 || len(ra) != len(rb) {
			t.Errorf("%s: %d refs vs %d", name, len(ra), len(rb))
		}
		for ref, h := range ra {
			if rb[ref] != h {
				t.Errorf("%s %s: %s vs %s", name, ref, h, rb[ref])
			}
		}
		fa, fb := fxFiles(t, filepath.Join(a, "acme-shop", name)), fxFiles(t, filepath.Join(b, "acme-shop", name))
		for p, body := range fa {
			if fb[p] != body {
				t.Errorf("%s/%s differs between runs", name, p)
			}
		}
		if len(fa) != len(fb) {
			t.Errorf("%s: %d files vs %d", name, len(fa), len(fb))
		}
	}
}

// TestFixtures_DetectionFacts checks the facts each toolchain module reads,
// since demo-gen is its own module and cannot import them. The same repos are
// detected by the real modules in internal/builds (see the Phase 5 report).
func TestFixtures_DetectionFacts(t *testing.T) {
	root, _ := genDemo(t, filepath.Join(t.TempDir(), "out"))
	proj := filepath.Join(root, "acme-shop")

	t.Run("LedgerIsDotnetSolution", func(t *testing.T) {
		files := fxFiles(t, filepath.Join(proj, "shop-ledger"))
		sln, ok := files["ShopLedger.sln"]
		if !ok {
			t.Fatal("no ShopLedger.sln at the repo root")
		}
		if !strings.Contains(sln, "\r\n") || strings.Contains(strings.ReplaceAll(sln, "\r\n", ""), "\n") {
			t.Error("the .sln must be CRLF only")
		}
		// Every project the solution names exists, and none needs a package.
		refs := regexp.MustCompile(`"([^"]+\.csproj)"`).FindAllStringSubmatch(sln, -1)
		if len(refs) != 2 {
			t.Fatalf("solution names %d projects, want 2", len(refs))
		}
		for _, m := range refs {
			body, ok := files[strings.ReplaceAll(m[1], `\`, "/")]
			if !ok {
				t.Errorf("solution names %s, which does not exist", m[1])
			}
			if strings.Contains(body, "PackageReference") {
				t.Errorf("%s references a NuGet package: restore would need the network", m[1])
			}
			if strings.Contains(body, "Microsoft.NET.Sdk.Web") {
				t.Errorf("%s uses the Web SDK", m[1])
			}
		}
		if !strings.Contains(files["tests/Ledger.Tests/Program.cs"], "return failures == 0 ? 0 : 1;") {
			t.Error("the test program must exit non-zero on failure")
		}
		if !strings.Contains(files[".gitignore"], "bin/") || !strings.Contains(files[".gitignore"], "obj/") {
			t.Error(".gitignore must ignore bin/ and obj/")
		}
	})

	t.Run("APIDevIsGoMain", func(t *testing.T) {
		files := fxFiles(t, filepath.Join(proj, "shop-api-dev"))
		if !strings.HasPrefix(files["go.mod"], "module ") {
			t.Error("go.mod missing")
		}
		if !regexp.MustCompile(`(?m)^package main$`).MatchString(files["main.go"]) {
			t.Error("main.go must declare package main so `go run .` is the dev command")
		}
		if strings.Contains(files["go.mod"], "require") {
			t.Error("the dev API must have no dependencies")
		}
		if !strings.Contains(files["main.go"], `os.Getenv("PORT")`) {
			t.Error("main.go does not read PORT")
		}
	})

	t.Run("WebDevHasDevScriptAndNoDeps", func(t *testing.T) {
		files := fxFiles(t, filepath.Join(proj, "shop-web-dev"))
		var pkg struct {
			Scripts      map[string]string `json:"scripts"`
			Dependencies map[string]string `json:"dependencies"`
			DevDeps      map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal([]byte(files["package.json"]), &pkg); err != nil {
			t.Fatal(err)
		}
		if pkg.Scripts["dev"] != "node server.js" {
			t.Errorf(`scripts.dev = %q, want "node server.js"`, pkg.Scripts["dev"])
		}
		if pkg.Scripts["test"] == "" {
			t.Error("no test script")
		}
		if len(pkg.Dependencies)+len(pkg.DevDeps) != 0 {
			t.Error("the dev web must have no dependencies")
		}
		// package-lock.json (npm) and not pnpm-lock.yaml, so no pnpm is needed.
		if _, ok := files["package-lock.json"]; !ok {
			t.Error("no package-lock.json")
		}
		if !strings.Contains(files["server.js"], "process.env.PORT") || !strings.Contains(files["server.js"], "YGG_PORT_1") {
			t.Error("server.js must read PORT and YGG_PORT_1")
		}
		if !strings.Contains(files["server.js"], "Listening on http://127.0.0.1:") {
			t.Error("server.js must print its listening line")
		}
	})
}

// TestFixtures_UVSecondState: shop-inventory has a branch with a changed
// uv.lock and pyproject.toml, for stale-venv tests.
func TestFixtures_UVSecondState(t *testing.T) {
	root, _ := genDemo(t, filepath.Join(t.TempDir(), "out"))
	r := openLocal(t, root, "shop-inventory")
	blob := func(branch, path string) string {
		h, err := r.ResolveRevision(plumbing.Revision(branch))
		if err != nil {
			t.Fatalf("resolve %s: %v", branch, err)
		}
		c, err := r.CommitObject(*h)
		if err != nil {
			t.Fatal(err)
		}
		f, err := c.File(path)
		if err != nil {
			t.Fatalf("%s:%s: %v", branch, path, err)
		}
		s, err := f.Contents()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, p := range []string{"uv.lock", "pyproject.toml"} {
		if blob("main", p) == blob("chore/bump-deps", p) {
			t.Errorf("%s is the same on main and chore/bump-deps", p)
		}
	}
	if !strings.Contains(blob("chore/bump-deps", "uv.lock"), "httpx") {
		t.Error("the branch lock file does not mention httpx")
	}
	// The conflicted merge state of main survives the extra branch.
	out := gitOut(t, filepath.Join(root, "acme-shop", "shop-inventory"), "status", "--porcelain=v2", "--branch")
	if !strings.Contains(out, "u UU ") || !strings.Contains(out, "# branch.head main") {
		t.Errorf("shop-inventory lost its conflict on main:\n%s", out)
	}
}

// TestFixtures_NoRealIdentities: the Phase 5 repos name no real host, person
// or machine path.
func TestFixtures_NoRealIdentities(t *testing.T) {
	root, _ := genDemo(t, filepath.Join(t.TempDir(), "out"))
	bad := []string{"gitlab.com", "github.com", "gitea.com", "bitbucket", "nuget.org", "npmjs", "C:\\", "/home/", "/Users/", "Authorization", "token", "password", "secret"}
	for _, name := range fxRepos {
		for p, body := range fxFiles(t, filepath.Join(root, "acme-shop", name)) {
			for _, s := range bad {
				if strings.Contains(strings.ToLower(body), strings.ToLower(s)) {
					t.Errorf("%s/%s mentions %q", name, p, s)
				}
			}
			for _, m := range regexp.MustCompile(`[\w.+-]+@[\w.-]+`).FindAllString(body, -1) {
				if !strings.HasSuffix(m, "@example.com") {
					t.Errorf("%s/%s has address %q", name, p, m)
				}
			}
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// copyTree copies the generated working tree (not .git) to dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	for p, body := range fxFiles(t, src) {
		full := filepath.Join(dst, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// startServer runs a dev server in dir with PORT and waits for its
// "Listening on" line. The process is killed at test end.
func startServer(t *testing.T, dir string, port int, name string, args ...string) {
	t.Helper()
	exe, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	cmd := exec.Command(exe, args...) //nolint:forbidigo // test of a fixture server, not product code
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port)) //nolint:forbidigo // the fixture server needs PATH to find go/node
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	want := fmt.Sprintf("Listening on http://127.0.0.1:%d", port)
	found := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == want {
				found <- true
				_, _ = io.Copy(io.Discard, stdout)
				return
			}
		}
		found <- false
	}()
	select {
	case ok := <-found:
		if !ok {
			t.Fatalf("%s %v exited without printing %q", name, args, want)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("%s %v did not print %q in time", name, args, want)
	}
}

func httpGet(t *testing.T, port int, path string) string {
	t.Helper()
	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("GET %s on %d: %d %s", path, port, res.StatusCode, b)
	}
	return string(b)
}

// TestFixtures_PairRunsSideBySide: two worktrees of the pair run together on
// different PORT values; each page and API answer names its own worktree and
// port, and the web proxy reaches the API of its own pair only. Skips without
// go and node.
func TestFixtures_PairRunsSideBySide(t *testing.T) {
	for _, tool := range []string{"go", "node"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	root, _ := genDemo(t, filepath.Join(t.TempDir(), "out"))
	proj := filepath.Join(root, "acme-shop")
	work := filepath.Join(t.TempDir(), "worktrees with space")

	type pair struct {
		wt       string
		api, web int
	}
	var pairs []pair
	for _, wt := range []string{"task-a", "task-b"} {
		// Like real worktrees, the folder name identifies the task.
		apiDir := filepath.Join(work, "api", wt)
		webDir := filepath.Join(work, "web", wt)
		copyTree(t, filepath.Join(proj, "shop-api-dev"), apiDir)
		copyTree(t, filepath.Join(proj, "shop-web-dev"), webDir)
		p := pair{wt: wt, api: freePort(t)}
		startServer(t, apiDir, p.api, "go", "run", ".")
		p.web = freePort(t)
		// The web server learns its API through YGG_PORT_1, like the block of
		// ports Yggdrasil gives a worktree.
		t.Setenv("YGG_PORT_1", fmt.Sprint(p.api))
		startServer(t, webDir, p.web, "node", "server.js")
		pairs = append(pairs, p)
	}
	if pairs[0].api == pairs[1].api || pairs[0].web == pairs[1].web {
		t.Fatal("ports collide")
	}
	for _, p := range pairs {
		page := httpGet(t, p.web, "/")
		if !strings.Contains(page, fmt.Sprintf(`id="worktree">%s<`, p.wt)) {
			t.Errorf("%s: page does not name its worktree folder:\n%s", p.wt, page)
		}
		if !strings.Contains(page, fmt.Sprintf(`id="port">%d<`, p.web)) {
			t.Errorf("%s: page does not name port %d:\n%s", p.wt, p.web, page)
		}
		var info struct{ Service, Port, Worktree string }
		if err := json.Unmarshal([]byte(httpGet(t, p.web, "/api/info")), &info); err != nil {
			t.Fatal(err)
		}
		if info.Port != fmt.Sprint(p.api) || info.Service != "shop-api-dev" || info.Worktree != p.wt {
			t.Errorf("%s: proxied info %+v, want port %d", p.wt, info, p.api)
		}
	}
}
