//go:build windows

package platform

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// bareConPTYSessions creates and closes n pseudo consoles with no process and
// returns how many handles the process gained. On Windows 11 22631 every
// CreatePseudoConsole/ClosePseudoConsole pair leaves one Process handle (the
// console host) behind; that is the OS, not StartPTY.
func bareConPTYSessions(t *testing.T, n int) int {
	t.Helper()
	one := func() {
		var inR, inW, outR, outW, hpc windows.Handle
		if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
			t.Fatal(err)
		}
		if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
			t.Fatal(err)
		}
		if err := windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 25}, inR, outW, 0, &hpc); err != nil {
			t.Fatal(err)
		}
		closeHandles(inR, outW)
		windows.ClosePseudoConsole(hpc)
		closeHandles(inW, outR)
	}
	one()
	before := handleCount(t)
	for range n {
		one()
	}
	time.Sleep(300 * time.Millisecond)
	return int(handleCount(t)) - int(before)
}

// StartPTY must not leak handles beyond what ConPTY itself leaves behind
// (spikes B1 problem 1: go-pty leaked the process handle of every start).
func TestPTYNoHandleLeak(t *testing.T) {
	n := 200
	if testing.Short() {
		n = 20
	}
	osLeak := bareConPTYSessions(t, n)

	argv := append(shellArgv(t, "cmd"), "/c", "exit", "0")
	one := func(closeFirst bool) {
		p, err := StartPTY(context.Background(), PTYSpec{Spec: Spec{Argv: argv, Env: ptyEnv()}, Size: Size{Cols: 80, Rows: 25}})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { defer close(done); drainPTY(p) }()
		if !closeFirst {
			if code, err := p.Wait(); err != nil || code != 0 {
				t.Fatalf("Wait = %d, %v", code, err)
			}
			<-done // EOF after the process exit, without Close
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		<-done
	}
	one(false)
	before := handleCount(t)
	for i := range n {
		one(i%2 == 0)
	}
	time.Sleep(300 * time.Millisecond)
	grew := int(handleCount(t)) - int(before)
	t.Logf("handles after %d sessions: StartPTY +%d, bare ConPTY +%d", n, grew, osLeak)
	// A per-session leak would add at least n; the slack covers runtime noise.
	if grew > osLeak+25 {
		t.Errorf("StartPTY gained %d handles over %d sessions, bare ConPTY only %d", grew, n, osLeak)
	}
}
