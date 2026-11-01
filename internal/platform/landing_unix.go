//go:build !windows

package platform

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func classifyErrno(e syscall.Errno) LockClass {
	switch e {
	case syscall.EBUSY, syscall.ETXTBSY:
		return LockSharing
	case syscall.EACCES, syscall.EPERM:
		return LockAccessDenied
	case syscall.ENAMETOOLONG:
		return LockPathTooLong
	}
	return LockOther
}

// moveDirOS renames; rename(2) replaces an empty directory, so the target is
// checked first (a race there can only replace an empty folder).
func moveDirOS(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("rename: %w", fs.ErrExist)
	}
	return os.Rename(from, to)
}

// LockHolders returns nil: there is no Restart Manager outside Windows. On
// Windows it is best effort and says only who may be using the folder.
func LockHolders(string) []string { return nil }

func fsPath(p string) string { return p }

func entryInfo(p string) (isLink, isDir bool, err error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return false, false, err
	}
	return fi.Mode()&os.ModeSymlink != 0, fi.IsDir(), nil
}

// prepStagedDir makes a folder readable and writable so its content can go.
func prepStagedDir(p string) { _ = os.Chmod(p, 0o700) }

func deleteEntry(p string, _ bool) error { return os.Remove(p) }
