//go:build windows

package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// A .cmd or .bat file (an npm shim such as claude.cmd) can't run on its own:
// cmd.exe runs it, and cmd.exe reads the command line as text. Left to
// CreateProcess, a command line that starts with a quote and holds more than
// two quotes loses its first and last one, so
//
//	"C:\Program Files\nodejs\claude.cmd" -p "a prompt"
//
// becomes C:\Program Files\nodejs\claude.cmd -p "a prompt" and cmd.exe fails
// with 'C:\Program' is not recognized. Start therefore runs batch files itself:
//
//	cmd.exe /d /s /c ""C:\Program Files\nodejs\claude.cmd" "-p" "a prompt""
//
// where /s strips exactly the outer pair, /d skips AutoRun commands, and every
// argument is quoted. A batch file is a shell, so what reaches it must be plain
// text; see batchArgOK.

// isBatchFile reports whether path is run by cmd.exe.
func isBatchFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		return true
	}
	return false
}

var errBatchArg = errors.New("platform: an argument can't be passed to a .cmd or .bat file safely")

// batchArgOK refuses what cmd.exe could read as something other than text. A
// quote ends the quoting, a percent sign expands a variable, line breaks end
// the command. The rest (& | < > ^ !) are literal inside the quotes we add,
// but a batch file is free to expand its arguments again without quotes
// (echo %~1), where & would start another command. We can't know what a batch
// file does, so they are refused outright. The arguments Yggdrasil passes
// (flags, model names, paths) never hold them. Free text such as a prompt goes
// on stdin, not on the command line.
func batchArgOK(arg string) bool {
	return !strings.ContainsAny(arg, "\"%\r\n\x00&|<>^!")
}

// quoteBatchArg wraps arg in quotes. Backslashes before the closing quote are
// doubled, as the C runtime of the program behind the batch file reads them as
// escapes for the quote.
func quoteBatchArg(arg string) string {
	trailing := len(arg) - len(strings.TrimRight(arg, `\`))
	return `"` + arg + strings.Repeat(`\`, trailing) + `"`
}

// batchCommandLine builds the command line that runs batchFile through the
// command interpreter at comspec. argv[0] is the name the caller gave and is
// left out; the batch file is called by its resolved path.
func batchCommandLine(comspec, batchFile string, argv []string) (string, error) {
	if !batchArgOK(batchFile) {
		return "", fmt.Errorf("%w: %q", errBatchArg, batchFile)
	}
	var b strings.Builder
	b.WriteString(`"` + comspec + `" /d /s /c ""` + batchFile + `"`)
	for _, a := range argv[min(1, len(argv)):] {
		if !batchArgOK(a) {
			return "", fmt.Errorf("%w (it holds a quote, a percent sign or a line break)", errBatchArg)
		}
		b.WriteString(" " + quoteBatchArg(a))
	}
	b.WriteString(`"`)
	return b.String(), nil
}
