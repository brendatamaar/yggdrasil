//go:build windows

package main

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

// conSys is the Windows side of goPTY: a job object for the process tree
// and our own handling of the pseudo console's lifetime.
//
// go-pty's Close calls ClosePseudoConsole and closes both pipes at once, so
// output still in the pipe is lost and Read fails with "file already
// closed". Instead we close only the pseudo console when the process exits
// (Read then drains the rest and gets EOF) and close the pipes ourselves.
// We never call go-pty's Close after that, or the HPCON would be closed twice.
type conSys struct {
	cp  gopty.ConPty
	job *job // nil with Options.NoJob

	consoleOnce sync.Once
}

var (
	procSetConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
	enableCtrlCOnce           sync.Once
)

// enableCtrlC clears this process's "ignore Ctrl+C" flag. Children inherit
// that flag, and a shell started with it set ignores the Ctrl+C that ConPTY
// generates from \x03. It is set whenever any ancestor was started with
// CREATE_NEW_PROCESS_GROUP or called SetConsoleCtrlHandler(NULL, TRUE).
func enableCtrlC() {
	enableCtrlCOnce.Do(func() {
		_, _, _ = procSetConsoleCtrlHandler.Call(0, 0)
	})
}

func startCmd(p gopty.Pty, cmd *gopty.Cmd, o Options) (ptySys, error) {
	cp, ok := p.(gopty.ConPty)
	if !ok {
		return nil, errors.New("go-pty did not return a ConPty")
	}
	enableCtrlC()
	s := &conSys{cp: cp}
	if o.NoJob {
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return s, nil
	}

	j, err := newJob()
	if err != nil {
		return nil, err
	}
	// Start suspended, assign to the job, then resume: no child can be
	// spawned before the process is in the job. go-pty closes the thread
	// handle from CreateProcess, so we find the thread again via Toolhelp.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := cmd.Start(); err != nil {
		j.close()
		return nil, err
	}
	pid := uint32(cmd.Process.Pid)
	if err := j.assign(pid); err != nil {
		_ = cmd.Process.Kill()
		j.close()
		return nil, err
	}
	s.job = j
	if err := resumeProcess(pid); err != nil {
		_ = j.terminate(1)
		j.close()
		return nil, err
	}
	return s, nil
}

func (s *conSys) closeConsole() {
	s.consoleOnce.Do(func() {
		windows.ClosePseudoConsole(windows.Handle(s.cp.Fd()))
	})
}

func (s *conSys) exited() {
	// The shell is gone; take its background children with it, like SIGHUP.
	if s.job != nil {
		_ = s.job.terminate(1)
	}
	s.closeConsole()
}

func (s *conSys) kill() error {
	if s.job != nil {
		return s.job.terminate(1)
	}
	// Without a job, closing the console is all we have: it sends
	// CTRL_CLOSE_EVENT to every process attached to it.
	s.closeConsole()
	return nil
}

func (s *conSys) release() error {
	s.closeConsole()
	if s.job != nil {
		s.job.close()
	}
	return errors.Join(s.cp.InputPipe().Close(), s.cp.OutputPipe().Close())
}

// job is a Windows job object that kills its processes when closed.
type job struct{ h windows.Handle }

func newJob() (*job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("set job limits: %w", err)
	}
	return &job{h: h}, nil
}

func (j *job) assign(pid uint32) error {
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(ph) }()
	if err := windows.AssignProcessToJobObject(j.h, ph); err != nil {
		return fmt.Errorf("assign process %d to job: %w", pid, err)
	}
	return nil
}

func (j *job) terminate(code uint32) error {
	if err := windows.TerminateJobObject(j.h, code); err != nil {
		return fmt.Errorf("terminate job: %w", err)
	}
	return nil
}

func (j *job) close() { _ = windows.CloseHandle(j.h) }

// resumeProcess resumes every thread of a process started with
// CREATE_SUSPENDED (there is exactly one).
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("thread snapshot: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return fmt.Errorf("open thread %d: %w", te.ThreadID, err)
		}
		_, err = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if err != nil {
			return fmt.Errorf("resume thread %d: %w", te.ThreadID, err)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("walk threads: %w", err)
	}
	if resumed == 0 {
		return fmt.Errorf("no threads found for process %d", pid)
	}
	return nil
}
