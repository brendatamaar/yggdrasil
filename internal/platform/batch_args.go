package platform

import (
	"errors"
	"fmt"
	"strings"
)

// ErrBatchArg is wrapped by the error BatchArgsOK and Start return for an
// argument that can't be passed to a .cmd or .bat file safely.
var ErrBatchArg = errors.New("platform: an argument can't be passed to a .cmd or .bat file safely")

// batchUnsafe is what cmd.exe could read as something other than text. A quote
// ends the quoting, a percent sign expands a variable, line breaks end the
// command. The rest (& | < > ^ !) are literal inside the quotes Start adds, but
// a batch file is free to expand its arguments again without quotes (echo
// %~1), where & would start another command. We can't know what a batch file
// does, so they are refused outright. The arguments Yggdrasil passes (flags,
// model names, paths) never hold them. Free text such as a prompt goes on
// stdin, not on the command line.
const batchUnsafe = "\"%\r\n\x00&|<>^!"

// describeBatchChar names a refused character for a user.
func describeBatchChar(r rune) string {
	switch r {
	case '\r', '\n':
		return "a line break"
	case 0:
		return "a NUL character"
	case '"':
		return `a double quote (")`
	case '%':
		return "a percent sign (%)"
	}
	return fmt.Sprintf("the character %q", r)
}

// BatchArgsOK checks argv against the rule Start applies to a .cmd or .bat
// file, without starting anything, so a command can be validated when it is
// saved (a build preset). The rule is the same on every OS, to keep presets
// portable. The error wraps ErrBatchArg and names the argument and the
// character.
func BatchArgsOK(argv []string) error {
	for i, a := range argv {
		if j := strings.IndexAny(a, batchUnsafe); j >= 0 {
			r := []rune(a[j:])[0]
			return fmt.Errorf("%w: argument %d holds %s. Split the command into separate steps or remove that character; "+
				"free text such as a prompt belongs on stdin, not on the command line",
				ErrBatchArg, i, describeBatchChar(r))
		}
	}
	return nil
}
