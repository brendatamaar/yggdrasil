package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// LockClass says why a rename or a delete of a folder failed, in terms the
// landing flow can act on (docs/design/landing.md). It is never empty.
type LockClass string

// The lock classes. LockOther is everything else, including a nil error.
const (
	// LockSharing: another process holds a file or folder open (Win32
	// ERROR_SHARING_VIOLATION 32 or ERROR_LOCK_VIOLATION 33).
	LockSharing LockClass = "sharing"
	// LockAccessDenied: access was refused (5), often a handle opened without
	// delete sharing, an antivirus scan or a read-only attribute.
	LockAccessDenied LockClass = "access_denied"
	// LockPathTooLong: the path is too long for the call (206).
	LockPathTooLong LockClass = "path_too_long"
	// LockOther: any other error.
	LockOther LockClass = "other"
)

// StagingDir is the folder under a worktree root that holds worktrees whose
// removal started. RemoveStaged only ever deletes below it.
const StagingDir = ".removing"

// ClassifyLock maps an error from a rename or delete to a LockClass. It looks
// through *os.LinkError, *os.PathError and errors.Join for a syscall.Errno.
// A nil or unknown error is LockOther.
func ClassifyLock(err error) LockClass {
	if err == nil {
		return LockOther
	}
	var en syscall.Errno
	if errors.As(err, &en) {
		return classifyErrno(en)
	}
	return LockOther
}

// MoveError is the error of MoveDir. Nothing was moved when it is returned.
type MoveError struct {
	Class LockClass
	Err   error
}

// Error implements error.
func (e *MoveError) Error() string {
	return fmt.Sprintf("move folder (%s): %v", e.Class, e.Err)
}

// Unwrap returns the cause.
func (e *MoveError) Unwrap() error { return e.Err }

// Errors of the landing helpers. Their text never holds a path.
var (
	// ErrMoveUnsupportedPath: a UNC, WSL or device path was given to MoveDir.
	ErrMoveUnsupportedPath = errors.New("only local folders on a drive letter can be moved; network, WSL and device paths are not supported")
	// ErrMoveTargetExists: MoveDir never replaces.
	ErrMoveTargetExists = errors.New("the target folder already exists")
	// ErrMoveSame: source and target are the same or nested, or a drive root.
	ErrMoveSame = errors.New("the target is the source folder or inside it, or a path is a drive root")
	// ErrStagedRefused: RemoveStaged was given a path it must not delete.
	ErrStagedRefused = errors.New("refusing to delete: the path is not a folder inside the removal staging area")
)

// MoveDir renames the folder from to to in one atomic step on the same
// volume. It never copies and never replaces: it fails when to exists. Both
// paths follow the package rules (absolute, no streams, no device names;
// UNC and WSL paths are refused) and deep paths use the \\?\ form. On failure
// the returned error is a *MoveError and nothing has changed. A rename fails
// at once when a process holds a handle inside the folder, which makes it a
// cheap lock probe (docs/design/landing.md).
func MoveDir(from, to string) error {
	f, err := cleanMovePath(from)
	if err != nil {
		return &MoveError{Class: LockOther, Err: err}
	}
	t, err := cleanMovePath(to)
	if err != nil {
		return &MoveError{Class: LockOther, Err: err}
	}
	if sameOrNested(f, t) {
		return &MoveError{Class: LockOther, Err: ErrMoveSame}
	}
	if err := moveDirOS(f, t); err != nil {
		return &MoveError{Class: ClassifyLock(err), Err: stripPath(err)}
	}
	return nil
}

func cleanMovePath(p string) (string, error) {
	if runtime.GOOS == "windows" {
		// \\?\C:\... is only a long-path spelling; the device namespaces are not.
		n := strings.ReplaceAll(p, "/", `\`)
		if strings.HasPrefix(n, `\\.\`) || strings.HasPrefix(n, `\??\`) {
			return "", ErrMoveUnsupportedPath
		}
	}
	c, err := CleanRootPath(p)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && strings.HasPrefix(c, `\\`) {
		return "", ErrMoveUnsupportedPath
	}
	if filepath.Dir(c) == c {
		return "", ErrMoveSame
	}
	return c, nil
}

func sameOrNested(from, to string) bool {
	if eqPath(from, to) {
		return true
	}
	pre := from + string(filepath.Separator)
	return len(to) > len(pre) && eqPath(to[:len(pre)], pre)
}

// Landing is what removing a worktree needs from the platform (RV-06,
// docs/design/landing.md). Fake: platformtest.FakeLanding; contract:
// platformtest.LandingPlatformContract.
type Landing interface {
	// MoveDir renames a folder atomically, never replacing (see MoveDir).
	MoveDir(from, to string) error
	// RemoveStaged deletes a staged folder tree (see RemoveStaged).
	RemoveStaged(root, staged string) (RemoveReport, error)
	// ClassifyLock classifies a failed rename or delete (see ClassifyLock).
	ClassifyLock(err error) LockClass
	// LockHolders names who may be using a folder (see LockHolders).
	LockHolders(path string) []string
}

// RealLanding is the Landing of this machine.
type RealLanding struct{}

// MoveDir implements Landing.
func (RealLanding) MoveDir(from, to string) error { return MoveDir(from, to) }

// RemoveStaged implements Landing.
func (RealLanding) RemoveStaged(root, staged string) (RemoveReport, error) {
	return RemoveStaged(root, staged)
}

// ClassifyLock implements Landing.
func (RealLanding) ClassifyLock(err error) LockClass { return ClassifyLock(err) }

// LockHolders implements Landing.
func (RealLanding) LockHolders(path string) []string { return LockHolders(path) }

// stripPath drops the path from *os.LinkError and *os.PathError so the text
// can be shown; the errno stays reachable with errors.As.
func stripPath(err error) error {
	var en syscall.Errno
	if errors.As(err, &en) {
		return en
	}
	if errors.Is(err, fs.ErrExist) {
		return ErrMoveTargetExists
	}
	return err
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// eqPath compares two cleaned paths, ASCII case-folded where the file system
// folds case (only ever stricter than the file system).
func eqPath(a, b string) bool {
	if FoldCase() {
		return asciiLower(a) == asciiLower(b)
	}
	return a == b
}

// stagedInside is the pure check behind RemoveStaged: staged must be a path
// strictly inside <root>/.removing, at least one level below it, with no ".."
// part and not a drive root. Both must be absolute. It judges text only; the
// caller resolves links and 8.3 names (see RemoveStaged).
func stagedInside(root, staged string) bool {
	if root == "" || staged == "" || strings.ContainsRune(root, 0) || strings.ContainsRune(staged, 0) {
		return false
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(staged) {
		return false
	}
	for _, seg := range strings.FieldsFunc(staged, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	r, s := filepath.Clean(root), filepath.Clean(staged)
	if filepath.Dir(r) == r || filepath.Dir(s) == s {
		return false
	}
	pre := filepath.Join(r, StagingDir) + string(filepath.Separator)
	if len(s) <= len(pre) || !eqPath(s[:len(pre)], pre) {
		return false
	}
	rel := s[len(pre):]
	// A ":" below the staging folder is an alternate data stream (or a drive
	// spelling) on Windows.
	if runtime.GOOS == "windows" && strings.Contains(rel, ":") {
		return false
	}
	return rel != "" && rel[0] != filepath.Separator
}
