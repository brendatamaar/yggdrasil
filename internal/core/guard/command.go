package guard

import "errors"

// ErrShellSyntax is returned for a command line with shell syntax we refuse:
// & | ; < > backtick $( %VAR% ^ newline, an unbalanced quote, or nothing.
var ErrShellSyntax = errors.New("the command line uses shell syntax that isn't allowed")

// ParseCommandLine splits a command line into argv, or refuses it. The shell
// sets Call.ParseErr when this returns an error.
//
// STUB (task G1 implements it): fails closed, every line is refused.
func ParseCommandLine(line string) ([]string, error) {
	_ = line
	return nil, ErrShellSyntax
}
