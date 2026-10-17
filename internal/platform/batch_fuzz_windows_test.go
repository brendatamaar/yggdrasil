//go:build windows

package platform

import (
	"strings"
	"testing"
)

// Accepted by BatchArgsOK => batchCommandLine builds a line with balanced
// quotes (each argument is wrapped in exactly one pair, plus the outer pair).
func FuzzBatchCommandLine(f *testing.F) {
	f.Add("-p", "a b", `C:\dir\`)
	f.Add("", `\`, `x\\`)
	f.Add("a&b", "%", "\"")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		argv := []string{"tool", a, b, c}
		if BatchArgsOK(argv) != nil {
			if _, err := batchCommandLine(`C:\Windows\System32\cmd.exe`, `C:\x\tool.cmd`, argv); err == nil {
				t.Fatalf("batchCommandLine accepted %q that BatchArgsOK refuses", argv)
			}
			return
		}
		line, err := batchCommandLine(`C:\Windows\System32\cmd.exe`, `C:\x\tool.cmd`, argv)
		if err != nil {
			t.Fatalf("accepted %q but batchCommandLine failed: %v", argv, err)
		}
		// comspec pair (2) + outer pair (2) + batch file (2) + one pair per argument (6)
		if n := strings.Count(line, `"`); n != 12 {
			t.Fatalf("unbalanced quotes (%d) in %s", n, line)
		}
	})
}
