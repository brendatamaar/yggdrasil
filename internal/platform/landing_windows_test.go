//go:build windows

package platform_test

import (
	"syscall"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// lockErrno returns an errno that classifies as c.
func lockErrno(c platform.LockClass) syscall.Errno {
	switch c {
	case platform.LockSharing:
		return 32
	case platform.LockAccessDenied:
		return 5
	case platform.LockPathTooLong:
		return 206
	}
	return 2
}

func TestClassifyLockWin32Table(t *testing.T) {
	for _, c := range []struct {
		errno syscall.Errno
		want  platform.LockClass
	}{
		{32, platform.LockSharing},
		{33, platform.LockSharing},
		{5, platform.LockAccessDenied},
		{206, platform.LockPathTooLong},
		{2, platform.LockOther},
		{3, platform.LockOther},
		{183, platform.LockOther},
		{145, platform.LockOther}, // directory not empty
		{111, platform.LockOther},
		{0, platform.LockOther},
	} {
		if got := platform.ClassifyLock(c.errno); got != c.want {
			t.Errorf("errno %d = %q, want %q", c.errno, got, c.want)
		}
	}
}
