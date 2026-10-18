//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// waitDelay bounds how long Wait lets a grandchild hold the output pipes open
// after the process itself has exited.
const waitDelay = 2 * time.Second

// Start starts spec in the main job, nested in its task job when spec.TaskID
// is set (spike B2). The process is created suspended, assigned to its jobs
// and only then resumed, so it cannot start a child that escapes. Cancelling
// ctx kills this process's tree only, never its siblings in the same task; to
// stop a whole task call KillTask.
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
	dir, err := prepareDir(spec.Dir)
	if err != nil {
		return nil, err
	}
	exe, err := lookPath(spec.Argv[0], dir, spec.Env)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{
		Path:        exe,
		Args:        spec.Argv,
		Dir:         dir,
		Env:         withNoCurDir(spec.Env),
		Stdin:       spec.Stdin,
		SysProcAttr: &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED},
		WaitDelay:   waitDelay,
	}
	if isBatchFile(exe) {
		// An npm shim (claude.cmd) is run by cmd.exe, which must be given the
		// command line in a form it reads correctly (see batch_windows.go).
		comspec, err := batchComspec(spec.Env)
		if err != nil {
			return nil, err
		}
		line, err := batchCommandLine(comspec, exe, spec.Argv)
		if err != nil {
			return nil, err
		}
		cmd.Path, cmd.Args = comspec, []string{comspec}
		cmd.SysProcAttr.CmdLine = line
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
	p := &process{cmd: cmd, outs: outs, done: make(chan struct{})}
	if err := p.begin(spec.TaskID); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		p.release()
		outs.abort()
		return nil, err
	}
	// Registered after the jobs exist, so a cancelled ctx fires on a process
	// that is already in them.
	p.stop = context.AfterFunc(ctx, func() { _ = p.Kill() })
	go p.run()
	return p, nil
}

// process is a Process in its own job.
type process struct {
	cmd  *exec.Cmd
	outs *outputs
	stop func() bool
	done chan struct{}
	code int
	err  error

	truncated bool // set before done is closed

	mu   sync.Mutex
	job  windows.Handle // own job; 0 once released
	task *taskJob
}

// begin puts the suspended process into its jobs and resumes it.
func (p *process) begin(taskID string) error {
	pid := p.cmd.Process.Pid
	h, err := openForJob(pid)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	own, task, err := joinJobs(h, taskID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.job, p.task = own, task
	p.mu.Unlock()
	return resumeProcess(pid)
}

// run waits for the process, then frees its jobs.
func (p *process) run() {
	err := p.cmd.Wait()
	if st := p.cmd.ProcessState; st != nil {
		p.code = st.ExitCode()
		var ee *exec.ExitError
		if errors.As(err, &ee) || errors.Is(err, exec.ErrWaitDelay) {
			err = nil
		}
	}
	// A grandchild that holds the pipe makes the output end late or never.
	p.truncated = !p.outs.drain(waitDelay)
	p.err = err
	p.stop()
	p.release()
	close(p.done)
}

// release closes the process job and drops its reference on the task job.
// Closing a job kills whatever is still in it.
func (p *process) release() {
	p.mu.Lock()
	job, task := p.job, p.task
	p.job, p.task = 0, nil
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
	p.mu.Unlock()
	if task != nil {
		releaseTask(task)
	}
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

func (p *process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == 0 {
		return nil
	}
	return terminateJob(p.job)
}
