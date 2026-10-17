//go:build windows

package platform

import (
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
// text; see BatchArgsOK.

// isBatchFile reports whether path is run by cmd.exe.
func isBatchFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		return true
	}
	return false
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
	// The batch file is called by its resolved path, so argv[0] is replaced by it.
	if err := BatchArgsOK(append([]string{batchFile}, argv[min(1, len(argv)):]...)); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(`"` + comspec + `" /d /s /c ""` + batchFile + `"`)
	for _, a := range argv[min(1, len(argv)):] {
		b.WriteString(" " + quoteBatchArg(a))
	}
	b.WriteString(`"`)
	return b.String(), nil
}

// batchComspec picks the command interpreter that runs batch files: COMSPEC
// from env when absolute, else SystemRoot\System32\cmd.exe. Shared by Start
// and StartPTY.
func batchComspec(env []string) (string, error) {
	comspec := envGet(env, "COMSPEC")
	if comspec == "" || !filepath.IsAbs(comspec) {
		comspec = filepath.Join(envGet(env, "SystemRoot"), "System32", "cmd.exe")
	}
	if !filepath.IsAbs(comspec) || !isRegular(comspec) {
		return "", fmt.Errorf("platform: the command interpreter %q was not found; set COMSPEC or SystemRoot", comspec)
	}
	return comspec, nil
}
