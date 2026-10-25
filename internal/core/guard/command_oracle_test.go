package guard

import (
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// bashWords is an independent, deliberately pessimistic model of what a POSIX
// shell does with one raw word (review gate 1, B1). It returns every word the
// shell may produce. A word it cannot predict (variable, glob, tilde) starts
// with wildMark: the shell may turn it into anything, including a flag.
const wildMark = "\x01"

func bashWords(raw string) []string {
	var out []string
	for _, r := range braceExpand(raw) {
		out = append(out, unquote(r))
	}
	return out
}

// braceExpand expands one level of an unquoted {a,b} group.
func braceExpand(s string) []string {
	var q byte
	open := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			q = c
		case c == '{':
			open = i
		case c == '}' && open >= 0:
			body := s[open+1 : i]
			if strings.Contains(body, ",") {
				var out []string
				for _, alt := range strings.Split(body, ",") {
					out = append(out, braceExpand(s[:open]+alt+s[i+1:])...)
				}
				return out
			}
			open = -1
		}
	}
	return []string{s}
}

func unquote(s string) string {
	var b strings.Builder
	wild := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case c == '\\':
			wild = true
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			j := i + 2
			for j < len(s) && s[j] != '\'' {
				if s[j] == '\\' && j+1 < len(s) {
					j++
					if d := s[j]; d >= '0' && d <= '7' {
						k := j
						for k < len(s) && k < j+3 && s[k] >= '0' && s[k] <= '7' {
							k++
						}
						n, _ := strconv.ParseInt(s[j:k], 8, 32)
						b.WriteByte(byte(n))
						j = k
						continue
					}
					b.WriteByte(s[j])
					j++
					continue
				}
				b.WriteByte(s[j])
				j++
			}
			i = j
		case c == '$':
			wild = true
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				wild = true
				i = len(s)
				break
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '$' || s[j] == '`' {
					wild = true
				}
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
				j++
			}
			i = j
		case c == '*' || c == '?' || c == '[':
			wild = true
		case c == '~' && b.Len() == 0:
			wild = true
		default:
			b.WriteByte(c)
		}
	}
	if wild {
		return wildMark + b.String()
	}
	return b.String()
}

// splitRaw splits a line on unquoted blanks without interpreting anything.
func splitRaw(line string) []string {
	var out []string
	var cur strings.Builder
	var q byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case q != 0:
			cur.WriteByte(c)
			if c == q {
				q = 0
			}
		case c == '\\' && i+1 < len(line):
			cur.WriteByte(c)
			i++
			cur.WriteByte(line[i])
		case c == '\'' || c == '"':
			q = c
			cur.WriteByte(c)
		case c == ' ' || c == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// checkAcceptedLine fails if the guard lets a `go test <tail>` line through
// although the modelled shell would run something other than what was
// judged: a word starting with "-", an unpredictable word, or a different
// argv.
func checkAcceptedLine(t *testing.T, tail string) {
	t.Helper()
	line := "go test " + tail
	argv, err := ParseCommandLine(line)
	if err != nil {
		return
	}
	call := execCall(argv)
	if !Decide(codePolicy(), call).Allow {
		return
	}
	var seen []string
	for _, raw := range splitRaw(line) {
		seen = append(seen, bashWords(raw)...)
	}
	if len(seen) != len(argv) {
		t.Fatalf("%q: shell makes %q, guard judged %q", line, seen, argv)
	}
	for i, w := range seen {
		if i >= 2 && (strings.HasPrefix(w, "-") || strings.HasPrefix(w, wildMark)) {
			t.Fatalf("%q allowed but the shell may run a flag word %q (judged %q)", line, w, argv)
		}
		if !strings.HasPrefix(w, wildMark) && w != argv[i] {
			t.Fatalf("%q: shell word %q differs from judged word %q", line, w, argv[i])
		}
	}
}

var escapeFragments = []string{
	`\-`, `\-x`, `$'\055'`, `$'\055x'`, `$'\x2dx'`, `{-x,y}`, `{a,-b}`, `"-"x`, `"-"`, `''-x`, `''`, `'-'`, `-`,
	`*`, `-*`, `*-x`, `?x`, `[-]x`, `$HOME`, `${x}`, `$1`, `~`, `~/x`, `a`, `pkg`, `./b`, `"a b"`, `'c d'`,
	`x"y"`, `\ `, `\\`, `\"`, `a\`, `#`, `!x`, `{a,b}`, `"$x"`, `@r`, `""-x`, `\@r`, `$"-x"`, `%5`,
}

func TestPropAcceptedLinesMatchShellModel(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 50; i++ {
			var words []string
			for n := 1 + r.Intn(3); n > 0; n-- {
				w := pick(r, escapeFragments...)
				if r.Intn(3) == 0 {
					w += pick(r, escapeFragments...)
				}
				words = append(words, w)
			}
			checkAcceptedLine(t, strings.Join(words, " "))
		}
	})
}

func TestEscapeFormsAreRefused(t *testing.T) {
	for _, f := range []string{`\-exec=evil.sh`, `$'\055exec=evil.sh'`, `{-exec=evil.sh,y}`, `"-"exec=evil.sh`, `''-exec=evil.sh`, `*-exec`} {
		argv, err := ParseCommandLine("go test " + f)
		if err == nil && Decide(codePolicy(), execCall(argv)).Allow {
			t.Errorf("go test %s was allowed as %q", f, argv)
		}
	}
}

func TestBashModelSelfCheck(t *testing.T) {
	for raw, want := range map[string][]string{
		`\-x`:      {"-x"},
		`$'\055x'`: {"-x"},
		`{-x,y}`:   {"-x", "y"},
		`"-"x`:     {"-x"},
		`''-x`:     {"-x"},
		`*`:        {wildMark},
		`a`:        {"a"},
	} {
		if got := bashWords(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("model(%q) = %q, want %q", raw, got, want)
		}
	}
}

func FuzzAcceptedLineMatchesShellModel(f *testing.F) {
	for _, s := range escapeFragments {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tail string) { checkAcceptedLine(t, tail) })
}
