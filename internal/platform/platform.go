package platform

import (
	"errors"
	"io"
)

// This file is the contract of the platform package: types shared by every
// OS implementation. The implementations live in build-tagged files
// (*_windows.go, *_darwin.go, *_linux.go). No code outside this package may
// start a process or check the OS (docs/AGENTS.md 5.2, 5.3).
//
// The package provides these functions (implemented per OS):
//
//	// Start starts a process in the main job (nested in the task job when
//	// spec.TaskID is set). It fails if spec.Env is nil. Cancelling ctx kills
//	// the process tree.
//	func Start(ctx context.Context, spec Spec) (Process, error)
//
//	// StartPTY starts a process attached to a pseudo-terminal, in a job too.
//	func StartPTY(ctx context.Context, spec PTYSpec) (PTY, error)
//
//	// OpenEditor and OpenFolder start the user's editor or file explorer
//	// outside every Yggdrasil job (PL-05), with a clean environment.
//	func OpenEditor(ctx context.Context, editor string, path string, env []string) error
//	func OpenFolder(ctx context.Context, path string, env []string) error
//
//	// Resolve finds an executable or project wrapper (mvnw.cmd vs ./mvnw,
//	// npm.cmd, a venv's python) for dir, honouring PATH and PATHEXT.
//	func Resolve(name, dir string, env []string) (string, error)
//
//	// LongPath expands 8.3 short names (GetLongPathNameW on Windows).
//	func LongPath(path string) (string, error)
//
//	// CheckPath refuses unsupported paths such as \\wsl$ (PL-04) with a
//	// plain-language error.
//	func CheckPath(path string) error
//
//	// ListeningPorts returns the TCP ports a process tree listens on (DV-02).
//	func ListeningPorts(pid int) ([]int, error)

// ErrNoEnv is returned by Start and StartPTY when Spec.Env is nil. The
// environment must come from internal/safety, never from the parent process
// (SF-04).
var ErrNoEnv = errors.New("platform: environment is required (use the clean environment from internal/safety)")

// Spec describes a process to start.
type Spec struct {
	// Argv[0] is the executable; resolve wrappers with Resolve first.
	Argv []string
	Dir  string
	// Env is the full environment; nil is an error (ErrNoEnv).
	Env []string
	// TaskID puts the process in that task's nested job, so cancelling the
	// task kills only its tree. Empty means the main job only.
	TaskID string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Process is a started process. Its tree dies with its job.
type Process interface {
	Pid() int
	// Wait blocks until the process exits and returns its exit code. A
	// non-zero exit code is not an error. It may be called more than once.
	Wait() (int, error)
	// Kill terminates the process tree.
	Kill() error
}

// Size is a terminal size in character cells.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// PTYSpec describes a process to start in a pseudo-terminal.
type PTYSpec struct {
	Spec
	Size Size
}

// PTY is a process attached to a pseudo-terminal (carried over from the
// Phase 0 spike, docs/spikes.md B1).
//
// Read returns terminal output. After the process has exited and the output
// is drained it returns an error (io.EOF or a closed-pipe error). Callers
// must keep reading until then: on Windows, closing the pseudo console can
// block while its output pipe is full.
type PTY interface {
	io.Reader
	io.Writer
	Resize(Size) error
	Pid() int
	// Wait blocks until the process exits and returns its exit code.
	Wait() (int, error)
	// Close kills the process tree and releases the terminal. Safe to call
	// more than once.
	Close() error
}
