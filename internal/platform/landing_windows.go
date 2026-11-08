//go:build windows

package platform

import (
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	errLockViolation   = syscall.Errno(33)  // ERROR_LOCK_VIOLATION
	errFilenameTooLong = syscall.Errno(206) // ERROR_FILENAME_EXCED_RANGE
)

func classifyErrno(e syscall.Errno) LockClass {
	switch e {
	case windows.ERROR_SHARING_VIOLATION, errLockViolation:
		return LockSharing
	case windows.ERROR_ACCESS_DENIED:
		return LockAccessDenied
	case errFilenameTooLong:
		return LockPathTooLong
	}
	return LockOther
}

// fsPath gives deep paths the \\?\ form so MAX_PATH does not apply.
func fsPath(p string) string {
	if len(p) >= longPathThreshold && !strings.HasPrefix(p, extendedPrefix) {
		return extendedPrefix + p
	}
	return p
}

// moveDirOS is MoveFileEx without MOVEFILE_COPY_ALLOWED and without
// MOVEFILE_REPLACE_EXISTING: a rename on one volume, failing if the target
// exists or if a handle inside the folder blocks it.
func moveDirOS(from, to string) error {
	f, err := windows.UTF16PtrFromString(fsPath(from))
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(fsPath(to))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(f, t, 0)
}

// entryInfo looks at the entry itself, never at a link target.
func entryInfo(p string) (isLink, isDir bool, err error) {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return false, false, err
	}
	attrs, err := windows.GetFileAttributes(u)
	if err != nil {
		return false, false, err
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0, nil
}

func prepStagedDir(string) {}

// deleteEntry clears the read-only attribute and deletes a file or an empty
// folder. A directory link (junction or symlink) is removed with
// RemoveDirectory, which removes the link and leaves the target alone.
func deleteEntry(p string, isDir bool) error {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	if attrs, err := windows.GetFileAttributes(u); err == nil && attrs&windows.FILE_ATTRIBUTE_READONLY != 0 {
		n := attrs &^ windows.FILE_ATTRIBUTE_READONLY
		if n == 0 {
			n = windows.FILE_ATTRIBUTE_NORMAL
		}
		_ = windows.SetFileAttributes(u, n)
	}
	if isDir {
		return windows.RemoveDirectory(u)
	}
	return windows.DeleteFile(u)
}
