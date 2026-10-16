//go:build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// StartPTY starts spec.Argv on a pseudo-terminal of spec.Size (creack/pty).
// The process leads its own session and process group; Close, and cancelling
// ctx, kill that group. spec.Stdin, Stdout and Stderr are not used. Ctrl+C is
// the byte 0x03 written to the terminal; the line discipline turns it into
// SIGINT for the foreground group.
func StartPTY(ctx context.Context, spec PTYSpec) (PTY, error) {
	if err := checkPTYSpec(spec); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := spec.Dir
	if dir != "" {
		if err := CheckPath(dir); err != nil {
			return nil, err
		}
		var err error
		if dir, err = LongPath(dir); err != nil {
			return nil, err
		}
	}
	exe, err := lookPath(spec.Argv[0], dir, spec.Env)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{Path: exe, Args: spec.Argv, Dir: dir, Env: spec.Env}
	// Setctty makes the terminal the controlling one, so job control and ^C work.
	master, err := pty.StartWithAttrs(cmd, winsize(spec.Size), &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", exe, err)
	}
	p := &unixPTY{master: master, cmd: cmd, done: make(chan struct{})}
	p.stop = context.AfterFunc(ctx, func() { _ = p.kill() })
	go p.run()
	return p, nil
}

func winsize(s Size) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(s.Cols), Rows: uint16(s.Rows)} //nolint:gosec // size is validated to 1..maxTermCells
}

type unixPTY struct {
	master *os.File
	cmd    *exec.Cmd
	stop   func() bool

	done chan struct{}
	code int
	err  error

	closeOnce sync.Once
	closeErr  error
}

func (p *unixPTY) run() {
	err := p.cmd.Wait()
	if st := p.cmd.ProcessState; st != nil {
		p.code = st.ExitCode()
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			err = nil
		}
	}
	p.err = err
	p.stop()
	// Take background children along, like the hangup of a closing terminal.
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGHUP)
	close(p.done)
}

// kill signals the session's process group (the child is its leader).
func (p *unixPTY) kill() error {
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group: %w", err)
	}
	return nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.master.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.master.Write(b) }
func (p *unixPTY) Pid() int                    { return p.cmd.Process.Pid }

func (p *unixPTY) Resize(s Size) error {
	if err := s.validate(); err != nil {
		return err
	}
	return pty.Setsize(p.master, winsize(s))
}

func (p *unixPTY) Wait() (int, error) {
	<-p.done
	return p.code, p.err
}

func (p *unixPTY) Close() error {
	p.closeOnce.Do(func() {
		killErr := p.kill()
		select {
		case <-p.done:
		case <-time.After(closeTimeout):
			p.closeErr = errors.Join(killErr, errors.New("platform: process did not exit after kill"))
			return
		}
		p.closeErr = errors.Join(killErr, p.master.Close())
	})
	return p.closeErr
}
