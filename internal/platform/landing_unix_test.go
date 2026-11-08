//go:build !windows

package platform_test

import (
	"syscall"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func lockErrno(c platform.LockClass) syscall.Errno {
	switch c {
	case platform.LockSharing:
		return syscall.EBUSY
	case platform.LockAccessDenied:
		return syscall.EACCES
	case platform.LockPathTooLong:
		return syscall.ENAMETOOLONG
	}
	return syscall.ENOENT
}

func TestClassifyLockUnixTable(t *testing.T) {
	for _, c := range []struct {
		errno syscall.Errno
		want  platform.LockClass
	}{
		{syscall.EBUSY, platform.LockSharing},
		{syscall.ETXTBSY, platform.LockSharing},
		{syscall.EACCES, platform.LockAccessDenied},
		{syscall.EPERM, platform.LockAccessDenied},
		{syscall.ENAMETOOLONG, platform.LockPathTooLong},
		{syscall.ENOENT, platform.LockOther},
		{syscall.EEXIST, platform.LockOther},
	} {
		if got := platform.ClassifyLock(c.errno); got != c.want {
			t.Errorf("errno %d = %q, want %q", c.errno, got, c.want)
		}
	}
}

func TestLockHoldersIsNilOffWindows(t *testing.T) {
	if got := platform.LockHolders(t.TempDir()); got != nil {
		t.Errorf("LockHolders = %v", got)
	}
}
