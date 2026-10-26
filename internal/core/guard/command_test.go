package guard

import (
	"reflect"
	"regexp"
	"testing"
)

func TestParseCommandLineAccepts(t *testing.T) {
	for line, want := range map[string][]string{
		"go test ./...":                  {"go", "test", "./..."},
		"  go   vet\t./... ":             {"go", "vet", "./..."},
		`go test "pkg/with space"`:       {"go", "test", "pkg/with space"},
		`go test 'a b' c`:                {"go", "test", "a b", "c"},
		`go test a"b c"d`:                {"go", "test", "ab cd"},
		`echo ""`:                        {"echo", ""},
		`go test -run 100%`:              {"go", "test", "-run", "100%"},
		"npm run test -- --reporter=dot": {"npm", "run", "test", "--", "--reporter=dot"},
		`mvnw.cmd -Dfoo=bar verify`:      {"mvnw.cmd", "-Dfoo=bar", "verify"},
		"go\ttest":                       {"go", "test"},
		`go test "a" "" b`:               {"go", "test", "a", "", "b"},
		`go test ''`:                     {"go", "test", ""},
		"go test C:/work/repo/file.go":   {"go", "test", "C:/work/repo/file.go"},
		"go test a=b:c+d":                {"go", "test", "a=b:c+d"},
		"go test one%two":                {"go", "test", "one%two"},
		`go test "-"x`:                   {"go", "test", "-x"}, // parsed; the tail rule refuses it
	} {
		got, err := ParseCommandLine(line)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %q, %v; want %q", line, got, err, want)
			continue
		}
		if p := checkArgvClean(got); p != "" {
			t.Errorf("%q: %s", line, p)
		}
	}
}

// Everything outside the word allowlist is refused (review gate 1, B1).
func TestParseCommandLineRefuses(t *testing.T) {
	for _, line := range []string{
		"", "   ", `go test "unterminated`, "go test 'a", `go test "a'b`, `go test 'a"b' "c`,
		"go test \x00", "go test a | b", "go test a\r", "go test a\nb", "go test \"multi\nline\"",
		"go test $(whoami)", "go test %PATH%", "go test 100%% done", "go test a^b", "go test `id`",
		"go test > out.txt", "go test < in.txt", "go test a && b", "go test &", "go test \"a\"; echo",
		// formerly accepted
		"go test $HOME", "go test ~/x", "go test (a)", "go test !x", "go test {a,b}", `go test a\ b`,
		`go test a\`, `go test \-exec=evil.sh`, `go test C:\work\repo\file.go`, `C:\tools\go.exe version`,
		`echo "it's"`, `echo '"q"'`, "go test é日本", "echo \"tab\tinside\"",
		`git commit -m "fix: a - b (c)"`,
		// expansions, ANSI-C quotes, globs, comments, history
		`go test $'\055x'`, `go test $"x"`, "go test ${HOME}", "go test $1", "go test *", "go test ?", "go test [a]",
		"go vet ./a,../..", "go test ./a,-exec=evil.exe", "go test @args", "go test a@b", "go test {-x,y}", "go test {1..3}", "go test #c", `go test "$HOME"`, `go test "${x}"`, `go test "a*"`,
		`go test "a\b"`, `go test "!x"`, `go test '~'`, `go test '$x'`, `go test "{a,b}"`,
	} {
		if argv, err := ParseCommandLine(line); err == nil {
			t.Errorf("accepted %q as %q", line, argv)
		}
	}
}

// One test per rejection rule (the Boundary table).
func TestParseCommandLineRejectsEachRule(t *testing.T) {
	rules := map[string]string{
		"ampersand":         "go test & go vet",
		"pipe":              "go test | cat",
		"semicolon":         "go test; go vet",
		"redirect out":      "go test > x",
		"redirect in":       "go test < x",
		"backtick":          "go test `x`",
		"command subst":     "go test $(x)",
		"percent variable":  "go test %VAR%",
		"caret":             "go test ^& x",
		"newline":           "go test\ngo vet",
		"carriage return":   "go test\rgo vet",
		"unbalanced double": `go test "x`,
		"unbalanced single": `go test 'x`,
		"empty":             "",
		"blank":             " \t ",
		"NUL":               "go\x00test",
		"quotes glue $(":    `go test $""(x)`,
		"backslash":         `go test \-x`,
		"dollar":            "go test $HOME",
		"ansi-c quote":      `go test $'\055x'`,
		"brace expansion":   "go test {-x,y}",
		"glob star":         "go test *",
		"glob question":     "go test ?x",
		"glob class":        "go test [a-z]",
		"tilde":             "go test ~",
		"bang":              "go test !!",
		"hash":              "go test #x",
		"paren":             "go test (x)",
		"non-ascii":         "go test é",
		"quote in quote":    `go test "a'b"`,
	}
	for name, line := range rules {
		t.Run(name, func(t *testing.T) {
			if argv, err := ParseCommandLine(line); err == nil {
				t.Fatalf("accepted as %q", argv)
			}
		})
	}
}

func TestFormatCommandLineRoundTrip(t *testing.T) {
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"echo", ""},
		{"echo", "a b", "c  d"},
		{"echo", " "},
		{"x", "100%"},
		{"go", "test", "-x"},
		{"C:/Program Files/go/bin/go.exe", "version"},
	} {
		line := FormatCommandLine(argv)
		got, err := ParseCommandLine(line)
		if err != nil || !reflect.DeepEqual(got, argv) {
			t.Errorf("%q -> %q -> %q, %v", argv, line, got, err)
		}
	}
}

var wordRe = regexp.MustCompile(`^[A-Za-z0-9_%+=:./ -]*$`)

// checkArgvClean returns a problem with an accepted argv, or "".
func checkArgvClean(argv []string) string {
	for _, a := range argv {
		if !wordRe.MatchString(a) {
			return "word outside the allowlist: " + a
		}
	}
	back, err := ParseCommandLine(FormatCommandLine(argv))
	if err != nil || !reflect.DeepEqual(back, argv) {
		return "join does not reparse to the same argv"
	}
	return ""
}
