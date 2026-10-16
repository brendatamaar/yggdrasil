//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procThreadAttributeJobList is PROC_THREAD_ATTRIBUTE_JOB_LIST, which x/sys
// does not define.
const procThreadAttributeJobList = 0x0002000D

// StartPTY starts spec.Argv in a ConPTY of spec.Size (docs/spikes.md B1, D12).
// The pseudo console and the job list go into ONE attribute list, so the
// process is created inside the main job (and the task job when spec.TaskID is
// set, plus a job of its own) and cannot spawn a child that escapes: no
// suspended start is needed. spec.Stdin, Stdout and Stderr are not used; the
// terminal is the I/O. Cancelling ctx kills the same tree as Start does,
// the whole task job when spec.TaskID is set.
//
// Close order: ClosePseudoConsole first, then drain the output pipe to EOF,
// then close both pipes. Callers must keep calling Read until it fails: the
// pseudo console can block while its output pipe is full.
//
// Ctrl+C decision: callers send 0x03 through Write and ConPTY turns it into
// CTRL_C_EVENT for the processes on the pseudo console. A child inherits the
// "ignore Ctrl+C" flag of ygg (set when an ancestor used CREATE_NEW_PROCESS_GROUP
// or SetConsoleCtrlHandler(NULL, TRUE), as under `go test` and many launchers),
// and then ignores the event. Clearing it for good, as the spike did, would make
// Ctrl+C in ygg's own console stop ygg. So we read the flag from the PEB and,
// only if it is set, clear it for the duration of CreateProcess and set it
// again (allowCtrlC). ygg's own flag is back as before within milliseconds.
// TestPTYCtrlC covers cmd and PowerShell.
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
	p, err := newConPTY(spec, exe, dir)
	if err != nil {
		return nil, err
	}
	// Registered after the jobs exist, so a cancelled ctx fires on a process
	// that is already in them.
	p.stop = context.AfterFunc(ctx, p.cancelTree)
	go p.run()
	return p, nil
}

// conPTY is a PTY backed by a Windows pseudo console.
type conPTY struct {
	in, out *os.File // we write keystrokes to in, read output from out
	pid     int
	proc    windows.Handle // process handle, closed by run
	stop    func() bool

	conMu  sync.RWMutex
	hpc    windows.Handle
	conEnd bool // hpc is closed

	mu   sync.Mutex
	job  windows.Handle // own job; 0 once released
	task *taskJob

	done chan struct{}
	code int
	err  error

	closeOnce sync.Once
	closeErr  error
}

func newConPTY(spec PTYSpec, exe, dir string) (*conPTY, error) {
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(spec.Argv))
	if err != nil {
		return nil, fmt.Errorf("platform: command line: %w", err)
	}
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return nil, fmt.Errorf("platform: program path: %w", err)
	}
	env, err := envBlock(spec.Env)
	if err != nil {
		return nil, err
	}
	var dirPtr *uint16
	if dir != "" {
		if dirPtr, err = windows.UTF16PtrFromString(dir); err != nil {
			return nil, fmt.Errorf("platform: working directory: %w", err)
		}
	}

	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, fmt.Errorf("create input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		closeHandles(inR, inW)
		return nil, fmt.Errorf("create output pipe: %w", err)
	}
	var hpc windows.Handle
	//nolint:gosec // size is validated to 1..maxTermCells
	size := windows.Coord{X: int16(spec.Size.Cols), Y: int16(spec.Size.Rows)}
	err = windows.CreatePseudoConsole(size, inR, outW, 0, &hpc)
	// The pseudo console holds its own copies of its ends.
	closeHandles(inR, outW)
	if err != nil {
		closeHandles(inW, outR)
		return nil, fmt.Errorf("create pseudo console: %w", err)
	}
	fail := func(err error) (*conPTY, error) {
		windows.ClosePseudoConsole(hpc)
		closeHandles(inW, outR)
		return nil, err
	}

	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return fail(fmt.Errorf("attribute list: %w", err))
	}
	defer attrs.Delete()
	// The pseudo console attribute's value is the HPCON itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, //nolint:gosec // raw Win32 attribute
		*(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		return fail(fmt.Errorf("set pseudo console attribute: %w", err))
	}

	var pi windows.ProcessInformation
	defer allowCtrlC()()
	own, task, err := startInJobs(spec.TaskID, func(list []windows.Handle) error {
		if err := attrs.Update(procThreadAttributeJobList,
			unsafe.Pointer(&list[0]), uintptr(len(list))*unsafe.Sizeof(list[0])); err != nil { //nolint:gosec // raw Win32 attribute
			return fmt.Errorf("set job list attribute: %w", err)
		}
		si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
		si.Cb = uint32(unsafe.Sizeof(si)) //nolint:gosec // fixed struct size
		// Null standard handles: without the flag a child can inherit our own
		// redirected stdio (a pipe, a log file) instead of the pseudo console.
		si.Flags = windows.STARTF_USESTDHANDLES
		flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
		if err := windows.CreateProcess(exePtr, cmdLine, nil, nil, false, flags, env, dirPtr, &si.StartupInfo, &pi); err != nil {
			return fmt.Errorf("start %s: %w", exe, err)
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	// Not suspended, so the thread handle has no further use.
	_ = windows.CloseHandle(pi.Thread)

	return &conPTY{
		in:   os.NewFile(uintptr(inW), "conpty-in"),
		out:  os.NewFile(uintptr(outR), "conpty-out"),
		pid:  int(pi.ProcessId),
		proc: pi.Process,
		hpc:  hpc,
		job:  own,
		task: task,
		done: make(chan struct{}),
	}, nil
}

var (
	procSetConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
	ctrlCMu                   sync.Mutex
)

// consoleIgnoreCtrlC is the CONSOLE_IGNORE_CTRL_C bit of the PEB's ConsoleFlags.
const consoleIgnoreCtrlC = 1

// allowCtrlC clears this process's "ignore Ctrl+C" flag if it is set, so a child
// created now does not inherit it, and returns the function that restores it.
// Call the result when CreateProcess is done. Calls are serialised.
func allowCtrlC() (restore func()) {
	ctrlCMu.Lock()
	peb := windows.RtlGetCurrentPeb()
	if peb == nil || peb.ProcessParameters == nil || peb.ProcessParameters.ConsoleFlags&consoleIgnoreCtrlC == 0 {
		return ctrlCMu.Unlock
	}
	_, _, _ = procSetConsoleCtrlHandler.Call(0, 0)
	return func() {
		_, _, _ = procSetConsoleCtrlHandler.Call(0, 1)
		ctrlCMu.Unlock()
	}
}

// startInJobs creates the process job (and the task job for taskID), then
// calls create with the job list [main, task?, own], outermost first, while
// holding the job lock so Shutdown cannot close a handle mid-call. If create
// fails, everything it set up is released. On success the caller owns the
// returned process job and the task reference.
func startInJobs(taskID string, create func(list []windows.Handle) error) (windows.Handle, *taskJob, error) {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	if jobs.closed {
		return 0, nil, errShutdown
	}
	if jobs.main == 0 {
		m, err := newJob()
		if err != nil {
			return 0, nil, err
		}
		jobs.main = m
	}
	list := []windows.Handle{jobs.main}
	var task *taskJob
	if taskID != "" {
		var err error
		if task, err = acquireTaskLocked(taskID); err != nil {
			return 0, nil, err
		}
		list = append(list, task.h)
	}
	own, err := newJob()
	if err == nil {
		list = append(list, own)
		if err = create(list); err != nil {
			_ = windows.CloseHandle(own)
		}
	}
	if err != nil {
		if task != nil {
			releaseTaskLocked(task)
		}
		return 0, nil, err
	}
	return own, task, nil
}

// envBlock builds a CreateProcess environment block: sorted, NUL-separated,
// ended by an extra NUL.
func envBlock(env []string) (*uint16, error) {
	sorted := slices.Clone(env)
	slices.SortFunc(sorted, func(a, b string) int {
		return strings.Compare(strings.ToUpper(a), strings.ToUpper(b))
	})
	var block []uint16
	for _, kv := range sorted {
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, fmt.Errorf("platform: environment entry: %w", err)
		}
		block = append(block, u...)
	}
	block = append(block, 0)
	if len(env) == 0 {
		block = append(block, 0)
	}
	return &block[0], nil
}

func closeHandles(hs ...windows.Handle) {
	for _, h := range hs {
		_ = windows.CloseHandle(h)
	}
}

// run waits for the process, then stops what it left behind and closes the
// pseudo console so Read drains the remaining output and reaches EOF.
func (p *conPTY) run() {
	_, _ = windows.WaitForSingleObject(p.proc, windows.INFINITE)
	var code uint32
	p.err = windows.GetExitCodeProcess(p.proc, &code)
	p.code = int(code)
	_ = windows.CloseHandle(p.proc)
	p.stop()
	p.release() // like SIGHUP: background children go with the shell
	p.closeConsole()
	close(p.done)
}

// release closes the process job and drops its reference on the task job.
// Closing a job kills whatever is still in it.
func (p *conPTY) release() {
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

func (p *conPTY) cancelTree() {
	p.mu.Lock()
	task := p.task
	p.mu.Unlock()
	if task != nil {
		_ = task.terminate()
		return
	}
	_ = p.kill()
}

func (p *conPTY) kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == 0 {
		return nil
	}
	return terminateJob(p.job)
}

func (p *conPTY) closeConsole() {
	p.conMu.Lock()
	defer p.conMu.Unlock()
	if !p.conEnd {
		p.conEnd = true
		windows.ClosePseudoConsole(p.hpc)
	}
}

func (p *conPTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *conPTY) Write(b []byte) (int, error) { return p.in.Write(b) }
func (p *conPTY) Pid() int                    { return p.pid }

func (p *conPTY) Resize(s Size) error {
	if err := s.validate(); err != nil {
		return err
	}
	p.conMu.RLock()
	defer p.conMu.RUnlock()
	if p.conEnd {
		return errors.New("platform: terminal is closed")
	}
	//nolint:gosec // size is validated to 1..maxTermCells
	return windows.ResizePseudoConsole(p.hpc, windows.Coord{X: int16(s.Cols), Y: int16(s.Rows)})
}

func (p *conPTY) Wait() (int, error) {
	<-p.done
	return p.code, p.err
}

// Close kills the tree, closes the pseudo console, drains and closes the pipes.
// Output not yet read by the caller is dropped.
func (p *conPTY) Close() error {
	p.closeOnce.Do(func() {
		killErr := p.kill()
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			_, _ = io.Copy(io.Discard, p.out)
		}()
		p.closeConsole()
		select {
		case <-p.done:
		case <-time.After(closeTimeout):
			p.closeErr = errors.Join(killErr, errors.New("platform: process did not exit after kill"))
			return
		}
		select {
		case <-drained:
		case <-time.After(closeTimeout):
			p.closeErr = errors.Join(killErr, errors.New("platform: terminal output did not reach EOF"))
			return
		}
		p.closeErr = errors.Join(killErr, p.in.Close(), p.out.Close())
	})
	return p.closeErr
}
