//go:build windows

package platform

import (
	"context"
	"testing"
)

// A batch file started through StartPTY follows the same rule as Start: cmd.exe
// gets a checked command line, and an argument it could reinterpret is refused.
func TestPTYBatchFile(t *testing.T) {
	script := writeBatch(t)
	for _, bad := range []string{"a&b", "100%", `a"b`, "x|y", "wow!"} {
		p, err := StartPTY(context.Background(), PTYSpec{Spec: Spec{Argv: []string{script, bad}, Env: ptyEnv()}, Size: Size{Cols: 80, Rows: 25}})
		if err == nil {
			_ = p.Close()
			t.Errorf("argument %q reached cmd.exe through StartPTY", bad)
		}
	}
	s := startArgvSession(t, []string{script, "one", "two words"}, "")
	s.waitFor(`A1=\[one\]`)
	s.waitFor(`A2=\[two words\]`)
	if code := s.wait(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}
