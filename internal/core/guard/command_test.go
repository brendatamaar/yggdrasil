package guard

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCommandLineAccepts(t *testing.T) {
	for line, want := range map[string][]string{
		"go test ./...":                    {"go", "test", "./..."},
		"  go   vet\t./... ":               {"go", "vet", "./..."},
		`go test "C:\Program Files\x\pkg"`: {"go", "test", `C:\Program Files\x\pkg`},
		`go test 'a b' c`:                  {"go", "test", "a b", "c"},
		`go test a"b c"d`:                  {"go", "test", "ab cd"},
		`echo ""`:                          {"echo", ""},
		`echo "it's"`:                      {"echo", "it's"},
		`echo '"q"'`:                       {"echo", `"q"`},
		`C:\tools\go.exe version`:          {`C:\tools\go.exe`, "version"},
		`go test -run 100%`:                {"go", "test", "-run", "100%"},
		"npm run test -- --reporter=dot":   {"npm", "run", "test", "--", "--reporter=dot"},
		`mvnw.cmd -Dfoo=bar verify`:        {"mvnw.cmd", "-Dfoo=bar", "verify"},
		"go test $HOME":                    {"go", "test", "$HOME"}, // no $( : left to the allowlist
		"go test ~/x":                      {"go", "test", "~/x"},
		`git commit -m "fix: a - b (c)"`:   {"git", "commit", "-m", "fix: a - b (c)"},
		"go\ttest":                         {"go", "test"},
		`go test "a" "" b`:                 {"go", "test", "a", "", "b"},
		`go test ''`:                       {"go", "test", ""},
		`go test "he said ""hi"" now"`:     {"go", "test", "he said hi now"},
		"go test é日本":                      {"go", "test", "é日本"},
		`go test C:\work\repo\file.go`:     {"go", "test", `C:\work\repo\file.go`},
		`go test (a)`:                      {"go", "test", "(a)"},
		"go test a=b":                      {"go", "test", "a=b"},
		"go test !x":                       {"go", "test", "!x"},
		`go test {a,b}`:                    {"go", "test", "{a,b}"},
		`go test a\ b`:                     {"go", "test", `a\`, "b"},
		`go test "a;b"`:                    nil, // refused below, listed here for the contrast
		"go test one%two":                  {"go", "test", "one%two"},
		`go test "unterminated`:            nil,
		"":                                 nil,
		"   ":                              nil,
		`go test &`:                        nil,
		"go test \x00":                     nil,
		"go test a | b":                    nil,
		"go test a\r":                      nil,
		"go test \"a\"; echo":              nil,
		"go test $(whoami)":                nil,
		"go test %PATH%":                   nil,
		"go test 100%% done":               nil,
		"go test a^b":                      nil,
		"go test `id`":                     nil,
		"go test > out.txt":                nil,
		"go test < in.txt":                 nil,
		"go test a && b":                   nil,
		"go test a\nb":                     nil,
		"go test 'a":                       nil,
		`go test "a'b`:                     nil,
		`go test 'a"b' "c`:                 nil,
		"powershell -c \"a\" ; \"b\"":      nil,
		"cmd /c dir & del x":               nil,
		`go test "$(x)"`:                   nil,
		`go test '$(x)'`:                   nil,
		`go test "a|b"`:                    nil,
		`go test "a>b"`:                    nil,
		"go test \"multi\nline\"":          nil,
		`echo "tab	inside"`:                {"echo", "tab\tinside"},
	} {
		got, err := ParseCommandLine(line)
		if want == nil {
			if err == nil {
				t.Errorf("%q accepted as %q", line, got)
			}
			continue
		}
		if p := checkArgvClean(got); err != nil || !reflect.DeepEqual(got, want) || p != "" {
			t.Errorf("%q = %q, %v; want %q", line, got, err, want)
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
	}
	for name, line := range rules {
		t.Run(name, func(t *testing.T) {
			if argv, err := ParseCommandLine(line); err == nil {
				t.Fatalf("accepted as %q", argv)
			}
		})
	}
	for _, line := range []string{`go test "a&b"`, `go test 'a|b'`, `go test "$(x)"`} {
		if _, err := ParseCommandLine(line); err == nil {
			t.Errorf("quoted metacharacter accepted: %q", line)
		}
	}
}

func TestFormatCommandLineRoundTrip(t *testing.T) {
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"echo", ""},
		{"echo", "a b", "c\td"},
		{"echo", `"`},
		{"echo", `"""`},
		{"echo", `it's`},
		{"echo", `it's "x" y`},
		{"echo", `'`},
		{"echo", `a"b'c"d`},
		{`C:\Program Files\go\bin\go.exe`, "version"},
		{"x", "100%"},
	} {
		line := FormatCommandLine(argv)
		got, err := ParseCommandLine(line)
		if err != nil || !reflect.DeepEqual(got, argv) {
			t.Errorf("%q -> %q -> %q, %v", argv, line, got, err)
		}
	}
}

// checkArgvClean returns a problem with an accepted argv, or "".
func checkArgvClean(argv []string) string {
	for _, a := range argv {
		if strings.ContainsAny(a, "&|;<>`^\n\r\x00") || strings.Contains(a, "$(") || strings.Count(a, "%") >= 2 {
			return "metacharacter in " + a
		}
	}
	back, err := ParseCommandLine(FormatCommandLine(argv))
	if err != nil || !reflect.DeepEqual(back, argv) {
		return "join does not reparse to the same argv"
	}
	return ""
}
