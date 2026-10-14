//go:build !windows

package main

import (
	"errors"
	"fmt"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/unix"
)

// unixSys is the Unix side of goPTY (creack/pty underneath). Minimal: it
// compiles and vets; Windows is the supported platform.
type unixSys struct {
	up  gopty.UnixPty
	pid int
}

func startCmd(p gopty.Pty, cmd *gopty.Cmd, _ Options) (ptySys, error) {
	up, ok := p.(gopty.UnixPty)
	if !ok {
		return nil, errors.New("go-pty did not return a UnixPty")
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// go-pty keeps the slave open in the parent, so the master would never
	// report EOF. The child has its own copy; drop ours.
	_ = up.Slave().Close()
	return &unixSys{up: up, pid: cmd.Process.Pid}, nil
}

// The child is a session leader (go-pty sets Setsid), so -pid is its group.
func (s *unixSys) exited() { _ = unix.Kill(-s.pid, unix.SIGHUP) }

func (s *unixSys) kill() error {
	if err := unix.Kill(-s.pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", s.pid, err)
	}
	return nil
}

func (s *unixSys) release() error { return s.up.Master().Close() }
