package git

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Invariant 1 (docs/AGENTS.md 6, SF-03): this package never implements force
// push, reset --hard, branch deletion, clean, worktree removal or a forced
// checkout. Invariant: every process starts through platform.Start.

var (
	// forbiddenName matches function, method and type names.
	forbiddenName = regexp.MustCompile(`(?i)force|reset|delete|remove|clean|prune|push|destroy|wipe`)
	// forbiddenWord matches words and options inside any string literal.
	forbiddenWord = regexp.MustCompile(`(?i)\b(reset|clean|push|remove|delete|prune|force)\b|--hard|--delete|--force|--prune|--mirror`)
	// forbiddenFlag matches short flag clusters holding f, d or D: -f, -fd, -d, -D.
	forbiddenFlag = regexp.MustCompile(`^-[a-zA-Z]*[fdD][a-zA-Z]*$`)
	// testWord is forbiddenWord for test files, which run real git in temp
	// repos: a plain `push` (to a local bare remote) is allowed there, but
	// nothing destructive and no discarding of changes.
	testWord = regexp.MustCompile(`(?i)\b(reset|clean|remove|delete|prune|force|discard)\b|--hard|--delete|--force|--prune|--mirror|--discard`)
)

// scanSource returns the violations in one non-test Go source file.
func scanSource(t *testing.T, name string, src any) []string {
	t.Helper()
	return scan(t, name, src, false)
}

// scan reports violations. In test files (tests true) function and type names
// are not checked and a plain push is allowed.
func scan(t *testing.T, name string, src any, tests bool) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
			bad = append(bad, name+": imports os/exec; start processes through platform.Start")
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if !tests && forbiddenName.MatchString(x.Name.Name) {
				bad = append(bad, name+": forbidden function "+x.Name.Name)
			}
		case *ast.TypeSpec:
			if !tests && forbiddenName.MatchString(x.Name.Name) {
				bad = append(bad, name+": forbidden type "+x.Name.Name)
			}
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				break
			}
			s, _ := strconv.Unquote(x.Value)
			word := forbiddenWord
			if tests {
				word = testWord
			}
			if word.MatchString(s) || forbiddenFlag.MatchString(s) {
				bad = append(bad, name+": forbidden string "+x.Value)
			}
		}
		return true
	})
	return bad
}

func TestNoDestructiveGit(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v, %d files", err, len(files))
	}
	var bad []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// This file lists the forbidden words on purpose.
		if f == "invariants_test.go" {
			continue
		}
		bad = append(bad, scan(t, f, src, strings.HasSuffix(f, "_test.go"))...)
	}
	for _, b := range bad {
		t.Error(b)
	}
}

// The scan must catch what it claims to catch.
func TestScanCatchesDestructiveCode(t *testing.T) {
	cases := map[string]string{
		"hard reset":    `package p; var a = []string{"reset", "--hard"}`,
		"force push":    `package p; var a = []string{"push", "--force-with-lease"}`,
		"branch -D":     `package p; var a = []string{"branch", "-D", "x"}`,
		"branch -d":     `package p; var a = []string{"branch", "-d", "x"}`,
		"clean":         `package p; var a = "clean"`,
		"checkout -f":   `package p; var a = []string{"checkout", "-f", "x"}`,
		"clustered -fd": `package p; var a = "-fd"`,
		"push delete":   `package p; var a = []string{"push", "--delete"}`,
		"worktree rm":   `package p; var a = []string{"worktree", "remove"}`,
		"func name":     `package p; func ForcePush() {}`,
		"method name":   `package p; type G struct{}; func (G) DeleteBranch() {}`,
		"os/exec":       `package p; import "os/exec"; var _ = exec.Command`,
	}
	for name, src := range cases {
		if len(scanSource(t, "case.go", src)) == 0 {
			t.Errorf("%s: scan found nothing", name)
		}
	}
	if bad := scanSource(t, "ok.go", `package p; var a = []string{"fetch", "-b", "--ff-only", "-z", "-c"}`); len(bad) != 0 {
		t.Errorf("false positive: %v", bad)
	}
	// Test files may push to a local bare remote, nothing more.
	if bad := scan(t, "ok_test.go", `package p; var a = []string{"push", "origin", "main"}`, true); len(bad) != 0 {
		t.Errorf("test file: plain push flagged: %v", bad)
	}
	for name, src := range map[string]string{
		"test force push":  `package p; var a = []string{"push", "--force"}`,
		"test hard reset":  `package p; var a = []string{"reset", "--hard"}`,
		"test discard":     `package p; var a = []string{"switch", "--discard-changes", "x"}`,
		"test os/exec":     `package p; import "os/exec"; var _ = exec.Command`,
		"test checkout -f": `package p; var a = []string{"checkout", "-f"}`,
	} {
		if len(scan(t, "bad_test.go", src, true)) == 0 {
			t.Errorf("%s: scan of a test file found nothing", name)
		}
	}
}

// A malformed status entry is an error, never a panic.
func TestParseStatusMalformed(t *testing.T) {
	for _, in := range []string{
		"1  a b c d e f g",
		"1 X a b c d e f g",
		"2  a b c d e f g h\x00orig",
		"1 .M",
		"u ",
	} {
		if _, err := parseStatus(in); err == nil {
			t.Errorf("parseStatus(%q) = nil error, want an error", in)
		}
	}
}

// Every git process goes through platform.Start, the package defines it once.
func TestEveryProcessStartsThroughPlatform(t *testing.T) {
	src, err := os.ReadFile("git.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "platform.Start("); n != 1 {
		t.Errorf("git.go calls platform.Start %d times, want exactly 1 (in run)", n)
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if f == "git.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "platform.Start(") {
			t.Errorf("%s starts a process outside run", f)
		}
	}
}
