//go:build windows

package agentstream

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// job is a job object holding one run's process tree. Minimal copy of
// spikes/jobobject (B2): KILL_ON_JOB_CLOSE, never BREAKAWAY_OK, a
// non-inheritable handle, per-child assignment (suspended start, assign,
// resume) so ygg itself stays outside the job.
type job struct {
	h    windows.Handle
	once sync.Once
}

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (not in x/sys).
type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func newJob() (*job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("set job limits: %w", err)
	}
	return &job{h: h}, nil
}

// startInGroup starts cmd suspended in a new job, assigns it and resumes it,
// so the process and everything it starts are in the job before it runs.
// CREATE_NEW_PROCESS_GROUP keeps console Ctrl+C away from the child: stopping
// it is our job (context cancel), not the console's.
func startInGroup(cmd *exec.Cmd) (group, error) {
	j, err := newJob()
	if err != nil {
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP
	if err := cmd.Start(); err != nil {
		_ = j.close()
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	if err := j.assignAndResume(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = j.close()
		return nil, err
	}
	return j, nil
}

func (j *job) assignAndResume(pid int) error {
	// os/exec holds its own process handle until Wait, so pid can't be reused.
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open pid %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(j.h, h); err != nil {
		return fmt.Errorf("assign pid %d to job: %w", pid, err)
	}
	return resumeProcess(pid)
}

// resumeProcess resumes the threads of pid. os/exec closes the main thread
// handle, so it's found again with a Toolhelp snapshot; a suspended new
// process has exactly one thread.
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

func (j *job) terminate() error {
	if err := windows.TerminateJobObject(j.h, 1); err != nil {
		return fmt.Errorf("terminate job: %w", err)
	}
	return nil
}

func (j *job) active() int {
	var info jobAccounting
	if err := windows.QueryInformationJobObject(j.h, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return -1
	}
	return int(info.ActiveProcesses)
}

// close closes the job handle; KILL_ON_JOB_CLOSE kills anything left in it.
func (j *job) close() error {
	var err error
	j.once.Do(func() { err = windows.CloseHandle(j.h) })
	return err
}
