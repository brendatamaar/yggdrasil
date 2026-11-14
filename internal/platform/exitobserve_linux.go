package platform

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// As in Go os/wait_waitid.go, WNOWAIT leaves the zombie (and PID) reserved.
// https://go.dev/src/os/wait_waitid.go
// https://man7.org/linux/man-pages/man2/waitpid.2.html
// Unlike os.Process, platform must not fall back to an unprotected Wait4.
type linuxExitObserver struct{ pid int }

func newExitObserver() (exitObserver, error) {
	// Self is not our child: ECHILD proves the exact primitive/options are
	// accepted before starting anything. ENOSYS/EPERM/etc fail closed.
	var info unix.Siginfo
	err := waitidExit(os.Getpid(), &info, unix.WNOHANG)
	if !errors.Is(err, unix.ECHILD) {
		if err == nil {
			return nil, errors.New("waitid capability: expected ECHILD, got success")
		}
		return nil, fmt.Errorf("waitid capability: %w", err)
	}
	return &linuxExitObserver{}, nil
}

func waitidExit(pid int, info *unix.Siginfo, flags int) error {
	for {
		err := unix.Waitid(unix.P_PID, pid, info, unix.WEXITED|unix.WNOWAIT|flags, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func (o *linuxExitObserver) arm(pid int) error {
	o.pid = pid
	var info unix.Siginfo
	return waitidExit(pid, &info, unix.WNOHANG)
}
func (o *linuxExitObserver) wait() error {
	var info unix.Siginfo
	return waitidExit(o.pid, &info, 0)
}
func (*linuxExitObserver) close() {}
