package platform

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Go stopped using Darwin waitid because WEXITED could report stops (#19314).
// EVFILT_PROC/NOTE_EXIT reports only exit, consumes no wait status and keeps the
// child unreaped. A stop/continue cannot close the signal gate.
// XNU kern_event.c filt_procattach/procevent and kern_exit.c implement this:
// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_event.c
// Registration's proc reference serializes attachment against exit. ESRCH means
// the process has entered exit (proc_find cannot acquire an exiting proc);
// with our sole reaper not yet running, its PID is still reserved. Treat it as
// ready. Cmd.Wait may finish kernel exit work, outside killMu. XNU proc_refdrain
// precedes NOTE_EXIT, so attachment either holds an earlier reference and sees
// that event, or fails ESRCH; the exit edge cannot be lost between those cases.
type darwinExitObserver struct {
	fd    int
	pid   int
	ready bool
}

func newExitObserver() (exitObserver, error) {
	// Serialize close-on-exec setup with os/exec's fork, as Kqueue has no CLOEXEC.
	syscall.ForkLock.Lock()
	fd, err := unix.Kqueue()
	if err == nil {
		unix.CloseOnExec(fd)
	}
	syscall.ForkLock.Unlock()
	if err != nil {
		return nil, err
	}
	o := &darwinExitObserver{fd: fd}
	// Check filter support before creating a child; discard this self watch.
	if err := o.change(os.Getpid(), unix.EV_ADD); err != nil {
		o.close()
		return nil, err
	}
	if err := o.change(os.Getpid(), unix.EV_DELETE); err != nil {
		o.close()
		return nil, err
	}
	return o, nil
}

func (o *darwinExitObserver) change(pid int, flags int) error {
	var ev unix.Kevent_t
	unix.SetKevent(&ev, pid, unix.EVFILT_PROC, flags)
	ev.Fflags = unix.NOTE_EXIT
	for {
		_, err := unix.Kevent(o.fd, []unix.Kevent_t{ev}, nil, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
func (o *darwinExitObserver) arm(pid int) error {
	o.pid = pid
	err := o.change(pid, unix.EV_ADD|unix.EV_ONESHOT)
	if errors.Is(err, unix.ESRCH) {
		o.ready = true
		return nil
	}
	return err
}
func (o *darwinExitObserver) wait() error {
	if o.ready {
		return nil
	}
	var events [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(o.fd, nil, events[:], nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 1 {
			ev := events[0]
			if ev.Flags&unix.EV_ERROR != 0 {
				return syscall.Errno(ev.Data)
			}
			if ev.Ident == uint64(o.pid) && ev.Filter == unix.EVFILT_PROC && ev.Fflags&unix.NOTE_EXIT != 0 {
				return nil
			}
			return fmt.Errorf("unexpected process exit event: %+v", ev)
		}
	}
}
func (o *darwinExitObserver) close() { _ = unix.Close(o.fd) }
