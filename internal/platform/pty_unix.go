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
	return startPTYObserved(ctx, spec, nil, newExitObserver)
}

func startPTYObserved(ctx context.Context, spec PTYSpec, hooks *ptyLifecycleHooks, makeObserver func() (exitObserver, error)) (PTY, error) {
	if err := checkPTYSpec(spec); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := prepareDir(spec.Dir)
	if err != nil {
		return nil, err
	}
	exe, err := lookPath(spec.Argv[0], dir, spec.Env)
	if err != nil {
		return nil, err
	}
	observer, err := makeObserver()
	if err != nil {
		return nil, fmt.Errorf("prepare PTY exit observer: %w", err)
	}
	defer func() {
		if observer != nil {
			observer.close()
		}
	}()
	cmd := &exec.Cmd{Path: exe, Args: spec.Argv, Dir: dir, Env: spec.Env}
	// Setctty makes the terminal the controlling one, so job control and ^C work.
	master, err := pty.StartWithAttrs(cmd, winsize(spec.Size), &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", exe, err)
	}
	if err := observer.arm(cmd.Process.Pid); err != nil {
		// Nothing has escaped: retain identity through the last group signal.
		killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		closeErr := master.Close()
		waitErr := cmd.Wait()
		return nil, fmt.Errorf("observe PTY exit: %w", errors.Join(err, killErr, closeErr, waitErr))
	}
	p := &unixPTY{master: master, cmd: cmd, done: make(chan struct{}), observer: observer, hooks: hooks}
	observer = nil
	p.unreg = registerTask(spec.TaskID, func() { _ = p.kill() })
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
	unreg  func()

	done chan struct{}
	code int
	err  error

	closeOnce sync.Once
	closeErr  error

	// killMu joins in-flight signals before closing the gate and reaping.
	// Neither native observation nor Cmd.Wait holds it (lifecycle_unix.go).
	killMu   sync.Mutex
	exited   bool
	observer exitObserver
	hooks    *ptyLifecycleHooks
}

// Per-PTY barriers observe real native signals and reaping; nil in production.
type ptyLifecycleHooks struct {
	beforeSignal func(syscall.Signal) error
	observed     func()
	beforeReap   func()
	reaped       func()
}

func (p *unixPTY) run() {
	observeErr := p.observer.wait()
	p.observer.close()
	if p.hooks != nil && p.hooks.observed != nil {
		p.hooks.observed()
	}
	p.killMu.Lock()
	if observeErr != nil {
		// Fail closed while the child still reserves its group identity.
		observeErr = errors.Join(observeErr, p.signalGroup(syscall.SIGKILL))
	}
	// Take background children along before releasing the leader's identity.
	hangupErr := p.signalGroup(syscall.SIGHUP)
	p.exited = true
	p.killMu.Unlock()
	if p.hooks != nil && p.hooks.beforeReap != nil {
		p.hooks.beforeReap()
	}
	err := p.cmd.Wait()
	if p.hooks != nil && p.hooks.reaped != nil {
		p.hooks.reaped()
	}
	if st := p.cmd.ProcessState; st != nil {
		p.code = st.ExitCode()
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			err = nil
		}
	}
	p.err = errors.Join(err, observeErr, hangupErr)
	p.stop()
	p.unreg()
	close(p.done)
}

// kill holds the signal gate from the guard through the syscall.
func (p *unixPTY) kill() error {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	if p.exited {
		return nil
	}
	return p.signalGroup(syscall.SIGKILL)
}

// signalGroup requires killMu, while identity is retained.
func (p *unixPTY) signalGroup(signal syscall.Signal) error {
	if p.hooks != nil && p.hooks.beforeSignal != nil {
		if err := p.hooks.beforeSignal(signal); err != nil {
			return err
		}
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal PTY process group: %w", err)
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
			p.closeErr = errors.Join(killErr, errors.New("platform: process did not exit after kill"), p.master.Close())
			return
		}
		p.closeErr = errors.Join(killErr, p.master.Close())
	})
	return p.closeErr
}
