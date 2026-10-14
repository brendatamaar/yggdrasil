//go:build windows

package jobobject

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procThreadAttributeJobList is PROC_THREAD_ATTRIBUTE_JOB_LIST
// (ProcThreadAttributeJobList = 13 | PROC_THREAD_ATTRIBUTE_INPUT), Windows 10+.
// x/sys/windows doesn't define it.
const procThreadAttributeJobList = 0x0002000D

var (
	modkernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procIsProcessInJob = modkernel32.NewProc("IsProcessInJob")
)

// Job is a Windows job object.
type Job struct {
	h        windows.Handle
	once     sync.Once
	closeErr error
}

// New creates an unnamed job object. Its handle is not inheritable: a child
// holding a copy would keep the job (and KillOnClose) alive after we die.
func New(opts Options) (*Job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if opts.KillOnClose {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	if opts.BreakawayOK {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("set job limits: %w", err)
	}
	return &Job{h: h}, nil
}

// Handle returns the raw job handle.
func (j *Job) Handle() windows.Handle { return j.h }

// Close closes the job handle. With KillOnClose this kills every process in
// the job, unless another handle to the job is still open.
func (j *Job) Close() error {
	j.once.Do(func() { j.closeErr = windows.CloseHandle(j.h) })
	return j.closeErr
}

// AssignSelf puts the current process into the job (approach a). Every child
// started afterwards inherits the job with no race.
func (j *Job) AssignSelf() error {
	if err := windows.AssignProcessToJobObject(j.h, windows.CurrentProcess()); err != nil {
		return fmt.Errorf("assign self to job: %w", err)
	}
	return nil
}

// AssignPID puts a running process into the job. Racy for a process that is
// already running: it may have started children before the call.
func (j *Job) AssignPID(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open pid %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(j.h, h); err != nil {
		return fmt.Errorf("assign pid %d to job: %w", pid, err)
	}
	return nil
}

// Terminate kills every process in the job, including nested jobs.
func (j *Job) Terminate(exitCode uint32) error {
	if err := windows.TerminateJobObject(j.h, exitCode); err != nil {
		return fmt.Errorf("terminate job: %w", err)
	}
	return nil
}

// Contains reports whether pid is in this job (directly or in a nested job).
func (j *Job) Contains(pid int) (bool, error) {
	return pidInJob(pid, j.h)
}

// PIDs lists the processes currently in the job.
func (j *Job) PIDs() ([]int, error) {
	var list struct {
		Assigned uint32
		InList   uint32
		IDs      [1024]uintptr
	}
	if err := windows.QueryInformationJobObject(j.h, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil); err != nil {
		return nil, fmt.Errorf("query job pids: %w", err)
	}
	pids := make([]int, 0, list.InList)
	for _, id := range list.IDs[:list.InList] {
		pids = append(pids, int(id))
	}
	return pids, nil
}

// Start starts cmd inside the job (approach b, os/exec flavour): CREATE_SUSPENDED,
// AssignProcessToJobObject, then resume. os/exec closes the main thread handle,
// so the thread is found again with a Toolhelp snapshot.
func (j *Job) Start(cmd *exec.Cmd) error {
	return startInJobs(cmd, j)
}

// InJob reports whether pid is in any job at all.
func InJob(pid int) (bool, error) {
	return pidInJob(pid, 0)
}

func pidInJob(pid int, job windows.Handle) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false, fmt.Errorf("open pid %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	return isProcessInJob(h, job)
}

// isProcessInJob wraps IsProcessInJob; job 0 means "any job".
func isProcessInJob(process, job windows.Handle) (bool, error) {
	var result int32
	r, _, err := procIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&result)))
	if r == 0 {
		return false, fmt.Errorf("IsProcessInJob: %w", err)
	}
	return result != 0, nil
}

// startInJobs starts cmd suspended, assigns it to each job in order (outermost
// first, so the inner ones nest inside it) and resumes it. On failure the
// process is killed before it ever ran.
func startInJobs(cmd *exec.Cmd, jobs ...*Job) error {
	addFlags(cmd, windows.CREATE_SUSPENDED)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	pid := cmd.Process.Pid
	if err := assignAndResume(pid, jobs); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return nil
}

func assignAndResume(pid int, jobs []*Job) error {
	// os/exec keeps its own process handle open until Wait, so pid can't be
	// reused while we hold it.
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open pid %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	for _, j := range jobs {
		in, err := isProcessInJob(h, j.h)
		if err != nil {
			return err
		}
		if in {
			continue // inherited, e.g. from a self-assigned parent
		}
		if err := windows.AssignProcessToJobObject(j.h, h); err != nil {
			return fmt.Errorf("assign pid %d to job: %w", pid, err)
		}
	}
	return resumeProcess(pid)
}

// resumeProcess resumes every thread of pid. A process created suspended has
// exactly one.
func resumeProcess(pid int) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("thread snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("open thread %d: %w", entry.ThreadID, err)
		}
		_, err = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if err != nil {
			return fmt.Errorf("resume thread %d: %w", entry.ThreadID, err)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("walk threads: %w", err)
	}
	if resumed == 0 {
		return fmt.Errorf("pid %d: no thread to resume", pid)
	}
	return nil
}

// Process is a process started with raw CreateProcess.
type Process struct {
	PID    int
	Handle windows.Handle
}

// Kill terminates the process.
func (p *Process) Kill() error { return windows.TerminateProcess(p.Handle, 1) }

// Close closes the process handle.
func (p *Process) Close() error { return windows.CloseHandle(p.Handle) }

// CreateProcess starts argv inside the job (approach b, raw flavour):
// windows.CreateProcess with CREATE_SUSPENDED, assign, ResumeThread on the
// returned thread handle. env nil means inherit ours.
func (j *Job) CreateProcess(argv, env []string) (*Process, error) {
	pi, err := createProcess(argv, env, windows.CREATE_SUSPENDED, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(pi.Thread)
	if err := windows.AssignProcessToJobObject(j.h, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Process)
		return nil, fmt.Errorf("assign pid %d to job: %w", pi.ProcessId, err)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Process)
		return nil, fmt.Errorf("resume pid %d: %w", pi.ProcessId, err)
	}
	return &Process{PID: int(pi.ProcessId), Handle: pi.Process}, nil
}

// StartInJobList starts argv already inside the job via
// PROC_THREAD_ATTRIBUTE_JOB_LIST (Windows 10+): no suspend/resume needed.
func (j *Job) StartInJobList(argv, env []string) (*Process, error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, fmt.Errorf("attribute list: %w", err)
	}
	defer attrs.Delete()
	jobs := []windows.Handle{j.h}
	if err := attrs.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]),
		uintptr(len(jobs))*unsafe.Sizeof(jobs[0])); err != nil {
		return nil, fmt.Errorf("set job list: %w", err)
	}
	pi, err := createProcess(argv, env, 0, attrs)
	if err != nil {
		return nil, err
	}
	_ = windows.CloseHandle(pi.Thread)
	return &Process{PID: int(pi.ProcessId), Handle: pi.Process}, nil
}

func createProcess(argv, env []string, flags uint32, attrs *windows.ProcThreadAttributeListContainer) (*windows.ProcessInformation, error) {
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return nil, err
	}
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return nil, err
	}
	var envBlock *uint16
	if env != nil {
		envBlock = &makeEnvBlock(env)[0]
	}
	si := new(windows.StartupInfoEx)
	si.Cb = uint32(unsafe.Sizeof(si.StartupInfo))
	// Null std handles: never leak our console or pipes (e.g. go test's
	// stdout, which it waits on) into the child.
	si.Flags = windows.STARTF_USESTDHANDLES
	if attrs != nil {
		si.Cb = uint32(unsafe.Sizeof(*si))
		si.ProcThreadAttributeList = attrs.List()
		flags |= windows.EXTENDED_STARTUPINFO_PRESENT
	}
	pi := new(windows.ProcessInformation)
	if err := windows.CreateProcess(app, cmdline, nil, nil, false,
		flags|windows.CREATE_UNICODE_ENVIRONMENT, envBlock, nil, &si.StartupInfo, pi); err != nil {
		return nil, fmt.Errorf("create process %s: %w", argv[0], err)
	}
	return pi, nil
}

// makeEnvBlock builds a UTF-16 "k=v\0k=v\0\0" block for CreateProcess.
func makeEnvBlock(env []string) []uint16 {
	var b []uint16
	for _, kv := range env {
		b = append(b, utf16.Encode([]rune(kv))...)
		b = append(b, 0)
	}
	if len(env) == 0 {
		b = append(b, 0)
	}
	return append(b, 0)
}

// Task is a job nested inside a parent job, tied to a context: cancelling the
// context terminates the task's whole tree and nothing else.
type Task struct {
	ctx    context.Context
	parent *Job
	job    *Job
	stop   func() bool
}

// NewTask creates a task job. Processes started with Task.Start join parent
// first (if they aren't in it already) and then the task job, which makes the
// task job nested inside parent. parent may be nil.
func NewTask(ctx context.Context, parent *Job) (*Task, error) {
	job, err := New(Options{KillOnClose: true})
	if err != nil {
		return nil, err
	}
	t := &Task{ctx: ctx, parent: parent, job: job}
	t.stop = context.AfterFunc(ctx, func() { _ = job.Terminate(1) })
	return t, nil
}

// Job returns the task's own job.
func (t *Task) Job() *Job { return t.job }

// Start starts cmd in the task's job.
func (t *Task) Start(cmd *exec.Cmd) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	jobs := []*Job{t.job}
	if t.parent != nil {
		jobs = []*Job{t.parent, t.job}
	}
	if err := startInJobs(cmd, jobs...); err != nil {
		return err
	}
	// Cancelled while starting: the AfterFunc may have run before the
	// process joined the job.
	if t.ctx.Err() != nil {
		_ = t.job.Terminate(1)
	}
	return nil
}

// Close stops watching the context and closes the task job, which kills any
// process still in it.
func (t *Task) Close() error {
	t.stop()
	return t.job.Close()
}

// errStillInJob: breakaway "succeeded" but the process is still in some job.
var errStillInJob = errors.New("still in a job after breakaway (an outer job forbids breakaway)")

// StartBreakaway starts cmd with CREATE_BREAKAWAY_FROM_JOB. Breakaway only
// leaves the jobs that allow it: under an outer job without BREAKAWAY_OK,
// CreateProcess still succeeds and the process silently stays in that outer
// job.
func StartBreakaway(cmd *exec.Cmd) error {
	addFlags(cmd, windows.CREATE_BREAKAWAY_FROM_JOB)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s with breakaway: %w", cmd.Path, err)
	}
	return nil
}

// StartOutside starts a process that must outlive us (PL-05).
//
//  1. CREATE_BREAKAWAY_FROM_JOB | CREATE_SUSPENDED, then check whether the
//     process is in any job before it runs. Not in a job: resume, done.
//  2. Still in a job (see StartBreakaway) or breakaway refused: kill it and
//     start again with brokerPID as its parent, so it joins the broker's jobs
//     instead of ours. Without a broker, a still-in-job process is kept.
//
// newCmd is called once per attempt because an exec.Cmd can't be reused.
func StartOutside(newCmd func() *exec.Cmd, brokerPID int) (OutsideResult, error) {
	cmd := newCmd()
	addFlags(cmd, windows.CREATE_BREAKAWAY_FROM_JOB|windows.CREATE_SUSPENDED)
	var why error
	switch err := cmd.Start(); {
	case err == nil:
		pid := cmd.Process.Pid
		in, err := InJob(pid)
		if err == nil && (!in || brokerPID == 0) {
			if err := resumeProcess(pid); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return OutsideResult{}, err
			}
			return OutsideResult{Cmd: cmd, Method: MethodBreakaway, StillInJob: in}, nil
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		why = errStillInJob
		if err != nil {
			why = err
		}
	case errors.Is(err, windows.ERROR_ACCESS_DENIED) && brokerPID != 0:
		why = err
	default:
		return OutsideResult{}, fmt.Errorf("start %s with breakaway: %w", cmd.Path, err)
	}
	cmd2 := newCmd()
	if err := StartWithParent(cmd2, brokerPID); err != nil {
		return OutsideResult{}, fmt.Errorf("breakaway: %w; broker: %w", why, err)
	}
	in, _ := InJob(cmd2.Process.Pid)
	return OutsideResult{Cmd: cmd2, Method: MethodBroker, BreakawayErr: why, StillInJob: in}, nil
}

func addFlags(cmd *exec.Cmd, flags uint32) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= flags
}

// StartWithParent starts cmd with parentPID as its parent process
// (PROC_THREAD_ATTRIBUTE_PARENT_PROCESS via SysProcAttr.ParentProcess). Needs
// PROCESS_CREATE_PROCESS and, for the std handles os/exec duplicates into the
// parent, PROCESS_DUP_HANDLE.
func StartWithParent(cmd *exec.Cmd, parentPID int) error {
	h, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE, false, uint32(parentPID))
	if err != nil {
		return fmt.Errorf("open broker pid %d: %w", parentPID, err)
	}
	defer windows.CloseHandle(h)
	addFlags(cmd, 0)
	cmd.SysProcAttr.ParentProcess = syscall.Handle(h)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s under pid %d: %w", cmd.Path, parentPID, err)
	}
	return nil
}

// ShellPID returns the PID of the desktop shell (explorer.exe), the natural
// broker for editors: it is outside our jobs and runs unelevated.
func ShellPID() (int, error) {
	hwnd := windows.GetShellWindow()
	if hwnd == 0 {
		return 0, errors.New("no shell window (no interactive desktop?)")
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil {
		return 0, fmt.Errorf("shell window pid: %w", err)
	}
	return int(pid), nil
}
