package guard

import (
	"errors"
	"strings"
)

// ErrShellSyntax is returned for a command line with shell syntax we refuse:
// & | ; < > backtick $( %VAR% ^ newline, an unbalanced quote, or nothing.
var ErrShellSyntax = errors.New("the command line uses shell syntax that isn't allowed")

// ParseCommandLine splits a command line into argv, or refuses it. The shell
// sets Call.ParseErr when this returns an error.
//
// The refused characters are refused everywhere, quoted or not, because cmd
// and PowerShell treat them differently from a POSIX shell. Backslash is an
// ordinary character (Windows paths); only ' and " group words.
func ParseCommandLine(line string) ([]string, error) {
	if strings.ContainsAny(line, "&|;<>`^\n\r\x00") || strings.Contains(line, "$(") ||
		strings.Count(line, "%") >= 2 {
		return nil, ErrShellSyntax
	}
	var (
		argv   []string
		cur    strings.Builder
		inWord bool
		quote  rune
	)
	flush := func() {
		if inWord {
			argv = append(argv, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for _, c := range line {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '"' || c == '\'':
			quote = c
			inWord = true
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, ErrShellSyntax
	}
	flush()
	if len(argv) == 0 {
		return nil, ErrShellSyntax
	}
	// Quotes can glue "$" and "(" together ($""( ), so look again at the result.
	for _, a := range argv {
		if strings.Contains(a, "$(") {
			return nil, ErrShellSyntax
		}
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
	if a == "" {
		return `""`
	}
	if !strings.ContainsAny(a, " \t\"'") {
		return a
	}
	// Runs without a double quote go inside "...", runs of double quotes go
	// inside '...'; adjacent pieces join into one word.
	var b strings.Builder
	for len(a) > 0 {
		isQuote := a[0] == '"'
		n := 0
		for n < len(a) && (a[n] == '"') == isQuote {
			n++
		}
		if isQuote {
			b.WriteString("'" + a[:n] + "'")
		} else {
			b.WriteString(`"` + a[:n] + `"`)
		}
		a = a[n:]
	}
	return b.String()
}
