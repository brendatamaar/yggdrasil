//go:build windows

package main

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// Check (a) output, (b) resize and (d) exit code with cmd.
func TestCmdEchoResizeExit(t *testing.T) {
	s := startSession(t, "cmd", Size{Cols: 80, Rows: 25}, false)
	s.send("echo ygg-pty-ok\r")
	s.waitFor(`(?m)^ygg-pty-ok`)

	s.send("mode con\r")
	s.waitFor(`Columns:\s+80\b`)
	if err := s.p.Resize(Size{Cols: 100, Rows: 30}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	s.send("mode con\r")
	s.waitFor(`Columns:\s+100\b`)

	s.send("exit 3\r")
	if code := s.wait(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

// Check (a) output, (b) resize and (d) exit code with PowerShell.
func TestPowerShellEchoResizeExit(t *testing.T) {
	s := startSession(t, "powershell", Size{Cols: 80, Rows: 25}, false)
	s.send("echo ygg-pty-ok\r")
	s.waitFor(`(?m)^ygg-pty-ok`)

	const size = `"ygg-size=" + $Host.UI.RawUI.WindowSize.Width + "x" + $Host.UI.RawUI.WindowSize.Height` + "\r"
	s.send(size)
	s.waitFor(`ygg-size=80x25`)
	if err := s.p.Resize(Size{Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("resize: %v", err)
	}
	s.send(size)
	s.waitFor(`ygg-size=120x40`)

	s.send("exit 3\r")
	if code := s.wait(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	if raw := s.raw(); strings.Contains(raw, "\x1b[?9001h") {
		t.Log("ConPTY requested win32-input-mode (CSI ?9001h)")
	}
}

// Check (a): Ctrl+C interrupts the running command, not the shell.
func TestCtrlC(t *testing.T) {
	for _, tc := range []struct{ shell, long string }{
		{"cmd", "ping -n 30 127.0.0.1"},
		{"powershell", "Start-Sleep -Seconds 30"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			s := startSession(t, tc.shell, Size{Cols: 80, Rows: 25}, false)
			s.send("echo ygg-ready\r")
			s.waitFor(`(?m)^ygg-ready`)
			s.send(tc.long + "\r")
			time.Sleep(1500 * time.Millisecond)
			start := time.Now()
			s.send("\x03")
			// Ctrl+C flushes the console input buffer, so don't type ahead.
			time.Sleep(500 * time.Millisecond)
			s.send("echo ygg-after-ctrlc\r")
			s.waitFor(`(?m)^ygg-after-ctrlc`)
			if d := time.Since(start); d > 10*time.Second {
				t.Fatalf("Ctrl+C took %v; the command was not interrupted", d)
			}
		})
	}
}

// Check (a): arrow keys reach the shell (Up recalls the last command).
func TestArrowKeys(t *testing.T) {
	t.Run("powershell", func(t *testing.T) {
		s := startSession(t, "powershell", Size{Cols: 80, Rows: 25}, false)
		s.send("$n = 0\r")
		s.send(`$n++; "ygg-n=$n"` + "\r")
		s.waitFor(`ygg-n=1`)
		s.send("\x1b[A\r")
		s.waitFor(`ygg-n=2`)
		// Left, Left, Right puts the cursor before "t": "echo ygg-lt" -> "echo ygg-left".
		s.send("echo ygg-lt\x1b[D\x1b[D\x1b[C" + "ef" + "\r")
		s.waitFor(`(?m)^ygg-left`)
	})
	t.Run("cmd", func(t *testing.T) {
		s := startSession(t, "cmd", Size{Cols: 80, Rows: 25}, false)
		s.send("set /a n+=1717\r")
		s.waitFor(`1717`)
		s.send("\x1b[A\r")
		s.waitFor(`3434`)
	})
}

// Check (c): interactive programs read keystrokes.
func TestInteractive(t *testing.T) {
	t.Run("powershell Read-Host", func(t *testing.T) {
		s := startSession(t, "powershell", Size{Cols: 80, Rows: 25}, false)
		s.send(`$x = Read-Host 'ygg-prompt'; "ygg-got-$x"` + "\r")
		s.waitFor(`ygg-prompt: `)
		s.send("abc\r")
		s.waitFor(`ygg-got-abc`)
	})
	t.Run("cmd choice", func(t *testing.T) {
		s := startSession(t, "cmd", Size{Cols: 80, Rows: 25}, false)
		s.send("choice /c yn /m ygg-choice\r")
		s.waitFor(`ygg-choice \[Y,N\]\?`)
		s.send("n")
		// choice flushes the input buffer when it exits; wait before typing on.
		s.waitFor(`ygg-choice \[Y,N\]\?N`)
		time.Sleep(200 * time.Millisecond)
		s.send("echo ygg-el=%errorlevel%\r")
		s.waitFor(`ygg-el=2`)
	})
}

// sgrBefore reports whether an SGR sequence setting red comes shortly before
// marker in the raw output. ConPTY re-encodes colours: legacy console
// attributes come out as 256-colour indexes (bright red is 38;5;9), VT
// written by the app may come out as 31.
func sgrBefore(raw, marker string) bool {
	i := strings.LastIndex(raw, marker)
	if i < 0 {
		return false
	}
	return regexp.MustCompile(`\x1b\[(?:[0-9]+;)*(?:31|91|38;5;9|38;5;1)(?:;[0-9]+)*m`).MatchString(raw[max(0, i-64):i])
}

// Check (f): unicode and colours come through.
func TestUnicodeAndColor(t *testing.T) {
	t.Run("powershell", func(t *testing.T) {
		s := startSession(t, "powershell", Size{Cols: 80, Rows: 25}, false)
		s.send(`"ygg-uni-$([char]0x00e9)$([char]0x4e16)"` + "\r")
		s.waitFor(`ygg-uni-é世`)
		s.send(`"ygg-emoji-" + [char]::ConvertFromUtf32(0x1F600)` + "\r")
		s.waitFor(`ygg-emoji-`)
		time.Sleep(300 * time.Millisecond)
		if !strings.Contains(s.raw(), "ygg-emoji-\U0001F600") {
			t.Logf("emoji (non-BMP) did not come through intact: %q", tail(s.raw(), 200))
		}
		s.send(`Write-Host ('ygg-' + 'red') -ForegroundColor Red` + "\r")
		s.waitFor(`(?m)^ygg-red`)
		if !sgrBefore(s.raw(), "ygg-red") {
			t.Fatalf("no red SGR before the coloured text: %q", tail(s.raw(), 200))
		}
	})
	t.Run("cmd", func(t *testing.T) {
		s := startSession(t, "cmd", Size{Cols: 80, Rows: 25}, false)
		s.send("set u=é世\r")
		s.send("echo ygg-uni-%u%\r")
		s.waitFor(`ygg-uni-é世`)
		// cmd has no colour command, but VT output from programs passes through.
		s.send("prompt $E[31mygg-red$E[0m$G\r")
		s.send("echo ygg-tick\r")
		s.waitFor(`ygg-red>`)
		if !sgrBefore(s.raw(), "ygg-red") {
			t.Fatalf("no red SGR before the prompt: %q", tail(s.raw(), 200))
		}
	})
}

// Check (e): Close kills the shell, its children and the pseudo console host.
func TestCloseKillsTree(t *testing.T) {
	for _, tc := range []struct {
		name  string
		noJob bool
	}{{"job", false}, {"no job", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := startSession(t, "cmd", Size{Cols: 80, Rows: 25}, tc.noJob)
			hosts := conhosts(t)
			t.Logf("console hosts owned by the test while the shell runs: %v", hosts)
			s.send("start /b ping -n 60 127.0.0.1 >nul\r")
			ping := waitForChild(t, s.p.Pid(), "PING.EXE")
			t.Cleanup(func() { killPid(ping) })

			start := time.Now()
			if err := s.p.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if !processGone(s.p.Pid(), 5*time.Second) {
				t.Fatalf("shell still running")
			}
			pingGone := processGone(ping, 5*time.Second)
			t.Logf("close took %v; grandchild gone: %v", time.Since(start), pingGone)
			if !tc.noJob && !pingGone {
				t.Fatalf("grandchild ping %d survived Close", ping)
			}
			deadline := time.Now().Add(5 * time.Second)
			for len(conhosts(t)) > 0 && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			if left := conhosts(t); len(left) > 0 {
				t.Fatalf("console hosts left after Close: %v", left)
			}
		})
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
