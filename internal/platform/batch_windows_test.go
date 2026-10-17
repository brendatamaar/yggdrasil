//go:build windows

package platform

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBatch writes a .cmd file that prints its first three arguments, one per
// line, into a folder whose name has a space (like C:\Program Files\nodejs).
func writeBatch(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Program Files", "shim dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "echoargs.cmd")
	script := "@echo off\r\necho A1=[%~1]\r\necho A2=[%~2]\r\necho A3=[%~3]\r\n"
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runBatch(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	p, err := Start(context.Background(), Spec{
		Argv: argv, Env: append(helperEnv(), "COMSPEC="+os.Getenv("COMSPEC")), Stdout: &out, Stderr: &out,
	})
	if err != nil {
		return "", err
	}
	code, err := p.Wait()
	if err != nil {
		return out.String(), err
	}
	if code != 0 {
		t.Logf("exit code %d, output: %s", code, out.String())
	}
	return out.String(), nil
}

// The shape of the real failure: a .cmd file in a folder with a space (an npm
// shim in C:\Program Files\nodejs) called with an argument that has a space.
// cmd.exe strips the outer quotes of such a command line and then cannot find
// 'C:\Program'.
func TestBatchFileInAFolderWithSpacesTakesArgumentsWithSpaces(t *testing.T) {
	script := writeBatch(t)
	out, err := runBatch(t, script, "hello world", "second arg", "plain")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"A1=[hello world]", "A2=[second arg]", "A3=[plain]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "is not recognized") {
		t.Errorf("cmd.exe could not find the batch file:\n%s", out)
	}
}

func TestBatchFileWithoutQuotedArguments(t *testing.T) {
	out, err := runBatch(t, writeBatch(t), "one", "two", "three")
	if err != nil || !strings.Contains(out, "A3=[three]") {
		t.Errorf("plain arguments: %q, %v", out, err)
	}
}

// Arguments that cmd.exe would read as something else are refused, never
// passed on: a batch file is a shell, not a program.
func TestBatchFileRefusesArgumentsCmdWouldReinterpret(t *testing.T) {
	script := writeBatch(t)
	// The batch file here prints %~1, which expands the argument without quotes:
	// an & in it would run a second command.
	for _, bad := range []string{`a"b`, "100%", "%PATH%", "x\ny", "x\ry", "x\x00y", "a & b", "c|d", "e>f", "g<h", "i^j", "wow!", "a&calc"} {
		out, err := runBatch(t, script, bad)
		if err == nil {
			t.Errorf("argument %q was passed to cmd.exe: %s", bad, out)
		}
	}
	// A bad argument anywhere in the list refuses the whole call.
	if _, err := runBatch(t, script, "fine", "also fine", "not&fine"); err == nil {
		t.Error("a bad third argument was passed on")
	}
	if _, statErr := os.Stat("f"); statErr == nil {
		t.Error("a redirection created a file")
	}
}

// Paths and flags such as Yggdrasil passes go through, parentheses included
// (C:\Program Files (x86)).
func TestBatchFileTakesOrdinaryPathsAndFlags(t *testing.T) {
	out, err := runBatch(t, writeBatch(t), "--settings", `C:\Users\Jane Doe\AppData\Roaming\ygg\runs\ygg-claude-1\settings.json`, `C:\Program Files (x86)\x`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"A1=[--settings]", `A2=[C:\Users\Jane Doe\AppData\Roaming\ygg\runs\ygg-claude-1\settings.json]`, `A3=[C:\Program Files (x86)\x]`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestBatchCommandLine(t *testing.T) {
	got, err := batchCommandLine(`C:\Windows\System32\cmd.exe`, `C:\Program Files\nodejs\claude.cmd`, []string{"claude", "-p", "a b", `C:\dir\`})
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Windows\System32\cmd.exe" /d /s /c ""C:\Program Files\nodejs\claude.cmd" "-p" "a b" "C:\dir\\""`
	if got != want {
		t.Errorf("command line =\n%s\nwant\n%s", got, want)
	}
	if _, err := batchCommandLine(`C:\Windows\System32\cmd.exe`, `C:\x"y\a.cmd`, nil); err == nil {
		t.Error("a quote in the batch file's path was accepted")
	}
}
