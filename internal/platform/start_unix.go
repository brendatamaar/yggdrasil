//go:build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Unix support is minimal (docs/AGENTS.md 5.2): each process leads its own
// process group, and killing the group kills the tree. There are no task
// jobs; KillTask walks a registry (taskreg_unix.go) and ctx cancel kills only that
// process's group.

const waitDelay = 2 * time.Second

// Start starts spec in a new process group; cancelling ctx kills the group.
func Start(ctx context.Context, spec Spec) (Process, error) {
	return startObserved(ctx, spec, nil, newExitObserver)
}

func startObserved(ctx context.Context, spec Spec, hooks *lifecycleHooks, makeObserver func() (exitObserver, error)) (Process, error) {
	if spec.Env == nil {
		return nil, ErrNoEnv
	}
	if len(spec.Argv) == 0 {
		return nil, errors.New("platform: Argv is empty")
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
		return nil, fmt.Errorf("prepare exit observer: %w", err)
	}
	// Ownership transfers to run only after Start and arm both succeed.
	defer func() {
		if observer != nil {
			observer.close()
		}
	}()
	cmd := &exec.Cmd{
		Path: exe, Args: spec.Argv, Dir: dir, Env: spec.Env,
		Stdin:       spec.Stdin,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
		WaitDelay:   waitDelay,
	}
	outs, err := newOutputs(cmd, spec.Stdout, spec.Stderr)
	if err != nil {
		return nil, fmt.Errorf("prepare output of %s: %w", exe, err)
	}
	if err := cmd.Start(); err != nil {
		outs.abort()
		return nil, fmt.Errorf("start %s: %w", exe, err)
	}
	outs.started()
	if err := observer.arm(cmd.Process.Pid); err != nil {
		// No Process or cancellation callback has escaped. The unreaped child
		// still owns the group identity; signal before calling Wait.
		killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr := cmd.Wait()
		outs.abort()
		return nil, fmt.Errorf("observe process exit: %w", errors.Join(err, killErr, waitErr))
	}
	p := &process{cmd: cmd, outs: outs, done: make(chan struct{}), observer: observer, hooks: hooks}
	observer = nil
	p.unreg = registerTask(spec.TaskID, func() { _ = p.Kill() })
	p.stop = context.AfterFunc(ctx, func() { _ = p.Kill() })
	go p.run()
	return p, nil
}

type process struct {
	cmd   *exec.Cmd
	outs  *outputs
	stop  func() bool
	unreg func()
	done  chan struct{}
	code  int
	err   error

	truncated bool // set before done is closed
	// exited rejects signals BEFORE identity is released; killMu joins every
	// signal already in flight. See lifecycle_unix.go for the ordering proof.
	observer   exitObserver
	hooks      *lifecycleHooks
	exited     atomic.Bool
	killMu     sync.Mutex
	killSent   bool
	terminated bool
}

func (p *process) run() {
	observeErr := p.observer.wait()
	p.observer.close()
	if p.hooks != nil && p.hooks.observed != nil {
		p.hooks.observed()
	}
	p.killMu.Lock()
	if observeErr != nil {
		// Fail closed: terminate while identity is retained, exclude further
		// signals, then reap without holding the cancellation mutex.
		observeErr = errors.Join(observeErr, p.signalGroup())
	}
	p.exited.Store(true)
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
		var ee *exec.ExitError
		if errors.As(err, &ee) || errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
	}
	p.killMu.Lock()
	if st := p.cmd.ProcessState; st != nil && p.killSent {
		if status, ok := st.Sys().(syscall.WaitStatus); ok {
			p.terminated = status.Signaled() && status.Signal() == syscall.SIGKILL
		}
	}
	p.killMu.Unlock()
	// A grandchild that holds the pipe makes the output end late or never.
	p.truncated = !p.outs.drain(waitDelay)
	p.err = errors.Join(err, observeErr)
	p.stop()
	p.unreg()
	close(p.done)
}

func (p *process) Pid() int { return p.cmd.Process.Pid }

func (p *process) Wait() (int, error) {
	<-p.done
	return p.code, p.err
}

func (p *process) Truncated() bool {
	<-p.done
	return p.truncated
}

func (p *process) terminatedByCaller() bool {
	<-p.done
	return p.terminated
}

func (p *process) Kill() error {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	if p.exited.Load() {
		return nil
	}
	if p.hooks != nil && p.hooks.beforeSignal != nil {
		if err := p.hooks.beforeSignal(); err != nil {
			return err
		}
	}
	return p.signalGroup()
}

// signalGroup requires killMu (or exclusive ownership before publication).
func (p *process) signalGroup() error {
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill process group: %w", err)
		}
	} else {
		p.killSent = true
	}
	return nil
}

// Shutdown does nothing on Unix: there is no main job.
func Shutdown() error { return nil }
