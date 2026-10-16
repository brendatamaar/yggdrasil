package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// skipDirs are folders that are not part of the root module's shipped code:
// separate modules (spikes, fixtures), the frontend and build output.
var skipDirs = []string{".git", ".task", "node_modules", "spikes", "fixtures", "web", "bin", "dist", ".claude", ".github"}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test folder")
		}
		dir = parent
	}
}

// sources parses every Go file of the root module.
func sources(t *testing.T) []file {
	t.Helper()
	root := moduleRoot(t)
	var files []file
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && slices.Contains(skipDirs, d.Name()) {
				return filepath.SkipDir
			}
			// A nested go.mod is another module (the demo generator, a spike).
			if p != root {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(rel)
		if dir == "." {
			dir = ""
		}
		f, err := parse(dir, d.Name(), mustRead(t, p))
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestArchitectureRules checks every rule against the real source tree.
func TestArchitectureRules(t *testing.T) {
	files := sources(t)
	if len(files) < 50 {
		t.Fatalf("only %d Go files found: the walk is broken, so the rules would pass for nothing", len(files))
	}
	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			for _, f := range files {
				for _, v := range r.check(f) {
					t.Error(v)
				}
			}
		})
	}
}

// The rules only count if they can fail: each one is run on source that breaks
// it and on source that does not.
func TestRulesCatchViolations(t *testing.T) {
	cases := []struct {
		rule string
		dir  string
		name string
		src  string
		want int
	}{
		{"os/exec", "internal/api", "x.go", `package api; import "os/exec"; var _ = exec.Command`, 1},
		{"os/exec", "internal/platform", "x.go", `package platform; import "os/exec"; var _ = exec.Command`, 0},
		{"os/exec", "internal/platform/sub", "x.go", `package sub; import "os/exec"; var _ = exec.Command`, 0},
		{"os/exec", "internal/platformx", "x.go", `package x; import "os/exec"; var _ = exec.Command`, 1},
		{"os/exec", "cmd/ygg", "x_test.go", `package main; import "os/exec"; var _ = exec.Command`, 1},
		{"os.StartProcess", "internal/git", "x.go", `package git; import "os"; var _, _ = os.StartProcess("a", nil, nil)`, 1},
		{"os.StartProcess", "internal/platform", "x.go", `package platform; import "os"; var _, _ = os.StartProcess("a", nil, nil)`, 0},
		{"syscall.StartProcess", "internal/builds", "x.go", `package builds; import "syscall"; var _, _, _ = syscall.StartProcess("a", nil, nil)`, 1},
		{"windows.CreateProcess", "internal/builds", "x.go", `package builds; import "golang.org/x/sys/windows"; var _ = windows.CreateProcess`, 1},
		{"runtime.GOOS", "internal/git", "x.go", `package git; import "runtime"; var _ = runtime.GOOS`, 1},
		{"runtime.GOOS", "internal/platform", "x.go", `package platform; import "runtime"; var _ = runtime.GOOS`, 0},
		{"runtime.GOOS", "internal/git", "x.go", `package git; import "runtime"; var _ = runtime.NumCPU()`, 0},
		{"os.Environ", "internal/builds", "x.go", `package builds; import "os"; var _ = os.Environ()`, 1},
		{"os.Environ", "internal/builds", "x.go", `package builds; import e "os"; var _ = e.Environ()`, 1},
		{"os.Environ", "internal/builds", "x.go", `package builds; import "os"; var _ = os.Getenv("A")`, 0},
		{"os.Environ", "internal/safety", "x.go", `package safety; import "os"; var _ = os.Environ()`, 0},
		{"syscall.Environ", "cmd/ygg", "x.go", `package main; import "syscall"; var _ = syscall.Environ()`, 1},
		{"InsertAudit", "internal/api", "x.go", `package api; func f(s interface{ InsertAudit() }) { s.InsertAudit() }`, 1},
		{"InsertAudit", "internal/audit", "x.go", `package audit; func f(s interface{ InsertAudit() }) { s.InsertAudit() }`, 0},
		{"InsertAudit", "internal/db", "x_test.go", `package db; func f(s interface{ InsertAudit() }) { s.InsertAudit() }`, 0},
		{"core is a leaf", "internal/core", "x.go", `package core; import _ "` + module + `internal/db"`, 1},
		{"core is a leaf", "internal/core", "x.go", `package core; import "strings"; var _ = strings.ToUpper`, 0},
		{"core is a leaf", "internal/core", "x_test.go", `package core; import _ "` + module + `internal/db"`, 0},
		{"safety is a leaf", "internal/safety", "x.go", `package safety; import _ "` + module + `internal/platform"`, 1},
		{"platform is a leaf", "internal/platform", "x.go", `package platform; import _ "` + module + `internal/safety"`, 1},
		{"db depends on core only", "internal/db", "x.go", `package db; import _ "` + module + `internal/api"`, 1},
		{"db depends on core only", "internal/db", "x.go", `package db; import _ "` + module + `internal/db/gen"; import _ "` + module + `internal/core"`, 0},
		{"db depends on core only", "internal/db", "x.go", `package db; import _ "` + module + `web"`, 1},
		{"sqlc output is a leaf", "internal/db/gen", "x.go", `package gen; import _ "` + module + `internal/core"`, 1},
		{"agents contract depends on core only", "internal/agents", "x.go", `package agents; import _ "` + module + `internal/api"`, 1},
		{"agents contract depends on core only", "internal/agents", "x_test.go", `package agents_test; import _ "` + module + `internal/agents/mock"`, 0},
		{"api does not import an adapter", "internal/api", "x.go", `package api; import _ "` + module + `internal/agents/claudecode"`, 1},
		{"api does not import an adapter", "internal/api", "x.go", `package api; import _ "` + module + `internal/agents"`, 0},
		{"api does not import an adapter", "internal/api", "x_test.go", `package api; import _ "` + module + `internal/agents/mock"`, 0},
		{"audit does not import api or agents", "internal/audit", "x.go", `package audit; import _ "` + module + `internal/agents"`, 1},
		{"git does not import the layers above it", "internal/git", "x.go", `package git; import _ "` + module + `internal/db"`, 1},
	}
	byName := map[string]rule{}
	for _, r := range rules {
		byName[r.name] = r
	}
	for _, c := range cases {
		r, ok := byName[c.rule]
		if !ok {
			t.Fatalf("no rule called %q", c.rule)
		}
		f, err := parse(c.dir, c.name, c.src)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if got := r.check(f); len(got) != c.want {
			t.Errorf("rule %q on %s/%s (%s): %d violation(s) %v, want %d", c.rule, c.dir, c.name, c.src, len(got), got, c.want)
		}
	}
	// Every rule is exercised by at least one violating case.
	for _, r := range rules {
		hit := false
		for _, c := range cases {
			hit = hit || (c.rule == r.name && c.want > 0)
		}
		if !hit {
			t.Errorf("rule %q has no test source that breaks it", r.name)
		}
	}
}

// Only one binary exists, and it is called ygg: other names belong to other
// software (docs/PRD.md section 11.1).
func TestOnlyTheYggBinaryExists(t *testing.T) {
	root := moduleRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if !slices.Equal(names, []string{"ygg"}) {
		t.Errorf("cmd/ holds %v, want only [ygg]", names)
	}
	for _, file := range []string{"Taskfile.yml", ".goreleaser.yaml"} {
		data := strings.ToLower(string(mustRead(t, filepath.Join(root, file))))
		for _, bad := range []string{"yggdrasil.exe", "yggd.exe", "yggctl", "yggdev", "-o bin/yggdrasil"} {
			if strings.Contains(data, bad) {
				t.Errorf("%s names a binary %q", file, bad)
			}
		}
	}
}

// The listen address defaults to loopback and the code that parses it refuses
// anything else (invariant 7). The server refuses a non-loopback listener too;
// api's tests cover that. This only guards the constant.
func TestDefaultAddressIsLoopback(t *testing.T) {
	src := string(mustRead(t, filepath.Join(moduleRoot(t), "cmd", "ygg", "config.go")))
	if !strings.Contains(src, `const defaultAddr = "127.0.0.1:`) {
		t.Error("cmd/ygg/config.go no longer defaults to 127.0.0.1")
	}
}
