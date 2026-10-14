package main

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	gopty "github.com/aymanbagabas/go-pty"
)

// ptySys is the OS-specific part of the go-pty implementation.
type ptySys interface {
	// exited runs once the process has exited: it stops leftover children
	// and makes Read return EOF after the remaining output.
	exited()
	// kill stops the whole process tree.
	kill() error
	// release frees the terminal. It runs after the process has exited.
	release() error
}

// goPTY implements PTY on top of github.com/aymanbagabas/go-pty.
type goPTY struct {
	pty gopty.Pty
	cmd *gopty.Cmd
	sys ptySys

	done    chan struct{}
	code    int
	waitErr error

	closeOnce sync.Once
	closeErr  error
}

var _ PTY = (*goPTY)(nil)

func startGoPTY(o Options) (PTY, error) {
	if err := o.Size.validate(); err != nil {
		return nil, err
	}
	p, err := gopty.New()
	if err != nil {
		return nil, fmt.Errorf("create pty: %w", err)
	}
	// go-pty always creates an 80x25 console; set the real size before the
	// process starts so it never sees the default.
	if err := p.Resize(o.Size.Cols, o.Size.Rows); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("set initial size: %w", err)
	}
	cmd := p.Command(o.Path, o.Args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	sys, err := startCmd(p, cmd, o)
	if err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("start %s: %w", o.Path, err)
	}
	g := &goPTY{pty: p, cmd: cmd, sys: sys, done: make(chan struct{})}
	go g.wait()
	return g, nil
}

func (g *goPTY) wait() {
	defer close(g.done)
	err := g.cmd.Wait()
	g.code = -1
	if g.cmd.ProcessState != nil {
		g.code = g.cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		g.waitErr = fmt.Errorf("wait: %w", err)
	}
	g.sys.exited()
}

func (g *goPTY) Read(b []byte) (int, error)  { return g.pty.Read(b) }
func (g *goPTY) Write(b []byte) (int, error) { return g.pty.Write(b) }
func (g *goPTY) Pid() int                    { return g.cmd.Process.Pid }

func (g *goPTY) Resize(s Size) error {
	if err := s.validate(); err != nil {
		return err
	}
	return g.pty.Resize(s.Cols, s.Rows)
}

func (g *goPTY) Wait() (int, error) {
	<-g.done
	return g.code, g.waitErr
}

func (g *goPTY) Close() error {
	g.closeOnce.Do(func() {
		killErr := g.sys.kill()
		select {
		case <-g.done:
		case <-time.After(5 * time.Second):
			g.closeErr = errors.Join(killErr, errors.New("process did not exit after kill"))
			return
		}
		g.closeErr = errors.Join(killErr, g.sys.release())
	})
	return g.closeErr
}
