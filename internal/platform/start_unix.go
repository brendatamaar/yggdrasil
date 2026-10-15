//go:build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// Unix support is minimal (docs/AGENTS.md 5.2): each process leads its own
// process group, and killing the group kills the tree. There are no task
// groups; Spec.TaskID is ignored and ctx cancel kills the process's tree.

const waitDelay = 2 * time.Second

// Start starts spec in a new process group; cancelling ctx kills the group.
func Start(ctx context.Context, spec Spec) (Process, error) {
	if spec.Env == nil {
		return nil, ErrNoEnv
	}
	if len(spec.Argv) == 0 {
		return nil, errors.New("platform: Argv is empty")
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
	cmd := &exec.Cmd{
		Path: exe, Args: spec.Argv, Dir: dir, Env: spec.Env,
		Stdin: spec.Stdin, Stdout: spec.Stdout, Stderr: spec.Stderr,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
		WaitDelay:   waitDelay,
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", exe, err)
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	p.stop = context.AfterFunc(ctx, func() { _ = p.Kill() })
	go p.run()
	return p, nil
}

type process struct {
	cmd  *exec.Cmd
	stop func() bool
	done chan struct{}
	code int
	err  error
}

func (p *process) run() {
	err := p.cmd.Wait()
	if st := p.cmd.ProcessState; st != nil {
		p.code = st.ExitCode()
		var ee *exec.ExitError
		if errors.As(err, &ee) || errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
	}
	p.err = err
	p.stop()
	close(p.done)
}

func (p *process) Pid() int { return p.cmd.Process.Pid }

func (p *process) Wait() (int, error) {
	<-p.done
	return p.code, p.err
}

func (p *process) Kill() error {
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group: %w", err)
	}
	return nil
}

// Shutdown does nothing on Unix: there is no main job.
func Shutdown() error { return nil }
