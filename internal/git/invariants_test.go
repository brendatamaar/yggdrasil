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

// nameForbidden checks a function or type name. In a writer file the word
// "push" is removed first when the name is in the pushNames allow-list.
func nameForbidden(n string, writer bool) bool {
	if writer && pushNames.MatchString(n) {
		n = strings.NewReplacer("Push", "", "push", "").Replace(n)
	}
	return forbiddenName.MatchString(n)
}

// scan reports violations. In test files (tests true) function and type names
// are not checked and a plain push is allowed.
func scan(t *testing.T, name string, src any, tests bool) []string {
	t.Helper()
	return scanMode(t, name, src, tests, false)
}

// pushNames are the only names that may hold the word "push" in the proposal
// writer files: names starting with Push, and classifyPush.
var pushNames = regexp.MustCompile(`^([Pp]ush|classifyPush$)`)

// scanWriter scans push.go and commit.go. Strings follow the test-file rule
// (a plain push is allowed), but function and type names stay checked, with
// the word "push" allowed only in pushNames.
func scanWriter(t *testing.T, name string, src any) []string {
	t.Helper()
	return scanMode(t, name, src, true, true)
}

func scanMode(t *testing.T, name string, src any, tests, writer bool) []string {
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
			if (!tests || writer) && nameForbidden(x.Name.Name, writer) {
				bad = append(bad, name+": forbidden function "+x.Name.Name)
			}
		case *ast.TypeSpec:
			if (!tests || writer) && nameForbidden(x.Name.Name, writer) {
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
		// These files list the forbidden words on purpose.
		if f == "invariants_test.go" || f == "argv_test.go" {
			continue
		}
		// The proposal writers (RV-02) may push and commit: strings follow the
		// test-file rule (force, reset, delete, remove, clean, prune, mirror
		// and discarding stay banned), names stay checked and may hold "push"
		// only through the pushNames allow-list.
		switch {
		case f == "push.go" || f == "commit.go":
			bad = append(bad, scanWriter(t, f, src)...)
		default:
			bad = append(bad, scan(t, f, src, strings.HasSuffix(f, "_test.go"))...)
		}
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
	// The writer files keep the name check: only Push-prefixed names and
	// classifyPush may say push.
	if bad := scanWriter(t, "ok.go", `package p; type PushRequest struct{}; func classifyPush() {}; func PushArgv() {}`); len(bad) != 0 {
		t.Errorf("writer file: allowed push names flagged: %v", bad)
	}
	for name, src := range map[string]string{
		"writer force name":    `package p; func ForcePush() {}`,
		"writer delete name":   `package p; func DeleteBranch() {}`,
		"writer other push":    `package p; func sendPush() {}`,
		"writer pushed delete": `package p; func PushDelete() {}`,
		"writer force string":  `package p; var a = "--force"`,
	} {
		if len(scanWriter(t, "bad.go", src)) == 0 {
			t.Errorf("%s: writer scan found nothing", name)
		}
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
	// commit.go runs the commit and push processes with its own call; it must
	// really be there (a positive check, not an exemption).
	cb, err := os.ReadFile("commit.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(cb), "platform.Start("); n != 1 {
		t.Errorf("commit.go calls platform.Start %d times, want exactly 1 (in exec)", n)
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if f == "git.go" || f == "commit.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "platform.Start(") {
			t.Errorf("%s starts a process outside run", f)
		}
	}
}
