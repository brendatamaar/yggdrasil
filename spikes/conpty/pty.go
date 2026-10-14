// Command conpty is the Phase 0 ConPTY spike (B1, D12): a browser terminal
// with xterm.js talking over a WebSocket to a shell in a PTY (ConPTY on
// Windows). Throwaway code; the findings feed internal/platform and DV-04.
package main

import (
	"fmt"
	"io"
)

// Size is a terminal size in character cells.
type Size struct {
	Cols int
	Rows int
}

func (s Size) validate() error {
	// ConPTY takes int16 coordinates; keep well inside that.
	if s.Cols < 1 || s.Cols > 1000 || s.Rows < 1 || s.Rows > 1000 {
		return fmt.Errorf("terminal size %dx%d out of range", s.Cols, s.Rows)
	}
	return nil
}

// Options describe the process to start in a PTY.
type Options struct {
	Path string
	Args []string
	Dir  string
	Env  []string // nil inherits the spike's environment
	Size Size

	// NoJob skips the job object on Windows. Spike only: it lets a test
	// compare process-tree cleanup with and without a job.
	NoJob bool
}

// PTY is a process attached to a pseudo-terminal.
//
// Read returns the terminal output. Once the process has exited and the
// output is drained, Read returns an error (io.EOF or a closed-pipe error).
// Callers must keep reading until then: on Windows, closing the pseudo
// console can block while its output pipe is full.
type PTY interface {
	io.Reader
	io.Writer
	// Resize changes the terminal size seen by the process.
	Resize(Size) error
	// Pid is the process ID of the started command.
	Pid() int
	// Wait blocks until the process exits and returns its exit code.
	// It may be called more than once.
	Wait() (int, error)
	// Close kills the process tree and releases the terminal.
	// It is safe to call more than once.
	Close() error
}

// Starter starts a command in a PTY. Implementations are swappable: go-pty
// is the default (D12), a hand-written ConPTY wrapper is the fallback.
type Starter func(Options) (PTY, error)
