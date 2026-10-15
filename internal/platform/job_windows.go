//go:build windows

package platform

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

var errShutdown = errors.New("platform: shut down, no more processes can start")

// jobs holds the process-wide job objects (docs/spikes.md B2). ygg itself
// never joins them: every child is assigned one by one, so editors can start
// outside, and nothing inside may break away (no BREAKAWAY_OK).
//
//	main job (KILL_ON_JOB_CLOSE)
//	 └─ task job, one per Spec.TaskID (shared by that task's processes)
//	     └─ process job, one per process
var jobs = struct {
	mu     sync.Mutex
	main   windows.Handle
	closed bool
	tasks  map[string]*taskJob
}{tasks: map[string]*taskJob{}}

// taskJob is open while refs > 0; refs and the handle are guarded by jobs.mu.
type taskJob struct {
	id   string
	h    windows.Handle
	refs int
}

// newJob creates an unnamed job that kills its processes when the last handle
// closes. The handle is not inheritable: a child holding a copy would keep
// the job alive after ygg dies.
func newJob() (windows.Handle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { //nolint:gosec // fixed struct size
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("set job limits: %w", err)
	}
	return h, nil
}

// Shutdown closes the main job, which kills every process Start created.
// Call it on graceful exit; if ygg dies instead, Windows does the same. Start
// fails afterwards.
func Shutdown() error {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	jobs.closed = true
	if jobs.main == 0 {
		return nil
	}
	h := jobs.main
	jobs.main = 0
	return windows.CloseHandle(h)
}

// joinJobs assigns the suspended process (handle h) to the main job, its task
// job (if taskID is set) and a new job of its own, outermost first so they
// nest. It returns the process job and the task job (nil without taskID),
// which now has one more reference.
func joinJobs(h windows.Handle, taskID string) (windows.Handle, *taskJob, error) {
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
	if err := windows.AssignProcessToJobObject(jobs.main, h); err != nil {
		return 0, nil, fmt.Errorf("assign process to the main job: %w", err)
	}
	var task *taskJob
	if taskID != "" {
		var err error
		if task, err = acquireTaskLocked(taskID); err != nil {
			return 0, nil, err
		}
		if err := windows.AssignProcessToJobObject(task.h, h); err != nil {
			releaseTaskLocked(task)
			return 0, nil, fmt.Errorf("assign process to task job: %w", err)
		}
	}
	own, err := newJob()
	if err == nil {
		if err = windows.AssignProcessToJobObject(own, h); err != nil {
			_ = windows.CloseHandle(own)
			err = fmt.Errorf("assign process to its job: %w", err)
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

func acquireTaskLocked(id string) (*taskJob, error) {
	t := jobs.tasks[id]
	if t == nil {
		h, err := newJob()
		if err != nil {
			return nil, err
		}
		t = &taskJob{id: id, h: h}
		jobs.tasks[id] = t
	}
	t.refs++
	return t, nil
}

func releaseTaskLocked(t *taskJob) {
	t.refs--
	if t.refs > 0 {
		return
	}
	if jobs.tasks[t.id] == t {
		delete(jobs.tasks, t.id)
	}
	_ = windows.CloseHandle(t.h) // kills anything still in the task
}

func releaseTask(t *taskJob) {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	releaseTaskLocked(t)
}

// terminate kills the task's tree. A no-op once the task job is closed.
func (t *taskJob) terminate() error {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	if t.refs == 0 {
		return nil
	}
	return terminateJob(t.h)
}

func terminateJob(h windows.Handle) error {
	if err := windows.TerminateJobObject(h, 1); err != nil {
		return fmt.Errorf("terminate job: %w", err)
	}
	return nil
}

// isProcessInJob wraps IsProcessInJob; job 0 means "any job".
func isProcessInJob(process, job windows.Handle) (bool, error) {
	var result int32
	r, _, err := procIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&result))) //nolint:gosec // raw Win32 call
	if r == 0 {
		return false, fmt.Errorf("IsProcessInJob: %w", err)
	}
	return result != 0, nil
}

// resumeProcess resumes every thread of pid. A process created suspended has
// exactly one. os/exec closes the thread handle, so the thread is found again
// with a Toolhelp snapshot.
func resumeProcess(pid int) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("thread snapshot: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))} //nolint:gosec // fixed struct size
	resumed := 0
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != uint32(pid) { //nolint:gosec // pids are positive 32-bit values
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

// openForJob opens a just-created process for job assignment and killing.
// os/exec keeps its own handle until Wait, so pid cannot be reused meanwhile.
func openForJob(pid int) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // pids are positive 32-bit values
	if err != nil {
		return 0, fmt.Errorf("open pid %d: %w", pid, err)
	}
	return h, nil
}
