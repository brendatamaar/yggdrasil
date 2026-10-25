package guard

import (
	"errors"
	"strings"
)

// ErrShellSyntax is returned for a command line with syntax we refuse: any
// character outside the word allowlist (see wordAllowed), an unbalanced
// quote, or nothing.
var ErrShellSyntax = errors.New("the command line uses shell syntax that isn't allowed")

// wordAllowed reports whether a is made only of the characters a word may
// hold: [A-Za-z0-9_@%+=:,./-] and, inside quotes, the space. It is the single
// definition used by ParseCommandLine (to refuse) and by the tail rule of the
// allowlist (to judge), so the word we judge is the word the shell sees: no
// character in it is special to bash, cmd or PowerShell, except that "%" is
// allowed once (a second "%" could close a cmd variable, see below).
func wordAllowed(a string) bool {
	for i := 0; i < len(a); i++ {
		if !wordByte(a[i]) && a[i] != ' ' {
			return false
		}
	}
	return strings.Count(a, "%") < 2
}

func wordByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("_@%+=:,./-", c) >= 0
}

// ParseCommandLine splits a command line into argv, or refuses it. The shell
// sets Call.ParseErr when this returns an error.
//
// A word may hold only the characters of wordAllowed, bare or inside '...' or
// "..." (a quoted word may also hold spaces). Backslash is refused: agents use
// forward slashes, and a backslash is an escape to bash and a path separator
// to cmd. $, braces, globs, parentheses, "!", "~", "#", ANSI-C quotes and
// every control character are refused too, so no expansion can make the shell
// run a word other than the one we judged. Quotes only group words; they are
// never part of one.
func ParseCommandLine(line string) ([]string, error) {
	var (
		argv   []string
		cur    strings.Builder
		inWord bool
		quote  byte
	)
	flush := func() {
		if inWord {
			argv = append(argv, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			switch {
			case c == quote:
				quote = 0
			case wordByte(c) || c == ' ':
				cur.WriteByte(c)
			default:
				return nil, ErrShellSyntax
			}
		case c == '"' || c == '\'':
			quote = c
			inWord = true
		case c == ' ' || c == '\t':
			flush()
		case wordByte(c):
			cur.WriteByte(c)
			inWord = true
		default:
			return nil, ErrShellSyntax
		}
	}
	if quote != 0 {
		return nil, ErrShellSyntax
	}
	flush()
	if len(argv) == 0 {
		return nil, ErrShellSyntax
	}
	// "%" pairs form a cmd variable (%PATH%), even across words: one per line.
	if strings.Count(line, "%") >= 2 {
		return nil, ErrShellSyntax
	}
	return argv, nil
}

// FormatCommandLine is the inverse of ParseCommandLine for any argv it can
// return: ParseCommandLine(FormatCommandLine(argv)) equals argv.
func FormatCommandLine(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = quoteWord(a)
	}
	return strings.Join(words, " ")
}

func quoteWord(a string) string {
	if a == "" || strings.Contains(a, " ") {
		return `"` + a + `"`
	}
	return a
}
