//go:build !windows

package platform

// Unix primitives of the safe filesystem capabilities: openat-style, component
// by component with O_NOFOLLOW from "/", exclusive creates (O_EXCL, mkdirat),
// identity = device:inode. Nothing here removes or truncates anything.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// NewSafeInitFS returns the real SafeInitFS of this platform.
func NewSafeInitFS() SafeInitFS { return safeInitFS{w: unixWalker{}} }

const (
	dirFlags  = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fileFlags = unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
)

type unixDir struct {
	fd int
	id DirIdentity
}

func (d *unixDir) ID() DirIdentity { return d.id }
func (d *unixDir) Close()          { _ = unix.Close(d.fd) }

// identityOf is device:inode of an open descriptor.
func identityOf(fd int) (DirIdentity, error) {
	var st unix.Stat_t
	if err := retryEINTR(func() error { return unix.Fstat(fd, &st) }); err != nil {
		return "", fmt.Errorf("read the folder identity: %w", err)
	}
	return DirIdentity(fmt.Sprintf("unix:%x:%x", uint64(st.Dev), uint64(st.Ino))), nil //nolint:unconvert // Dev is int32 on darwin
}

func retryEINTR(f func() error) error {
	for {
		if err := f(); !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func openat(dirfd int, name string, flags int, mode uint32) (int, error) {
	for {
		fd, err := unix.Openat(dirfd, name, flags, mode)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

// isLinkErr reports the errors a no-follow open gives for a symlink or a
// non-folder where a folder was expected.
func isLinkErr(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EMLINK)
}

func wrapDirErr(err error, name string) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return fmt.Errorf("the folder %q does not exist: %w", name, fs.ErrNotExist)
	case isLinkErr(err):
		return fmt.Errorf("%q is a link or not a folder; Yggdrasil never writes through links: %w", name, ErrUnsafePath)
	}
	return fmt.Errorf("open %q: %w", name, err)
}

func newUnixDir(fd int) (*unixDir, error) {
	id, err := identityOf(fd)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &unixDir{fd: fd, id: id}, nil
}

func (d *unixDir) OpenDir(leaf string) (fsDir, error) {
	fd, err := openat(d.fd, leaf, dirFlags, 0)
	if err != nil {
		return nil, wrapDirErr(err, leaf)
	}
	return newUnixDir(fd)
}

func (d *unixDir) Mkdir(leaf string) (fsDir, error) {
	err := retryEINTR(func() error { return unix.Mkdirat(d.fd, leaf, 0o755) })
	switch {
	case errors.Is(err, unix.EEXIST):
		return nil, ErrExists
	case err != nil:
		return nil, err
	}
	// The new folder is ours, but open it without following anything anyway:
	// a racing process could already have swapped the name.
	return d.OpenDir(leaf)
}

func (d *unixDir) CreateFile(name string) (*os.File, error) {
	fd, err := openat(d.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|fileFlags, 0o644)
	switch {
	case errors.Is(err, unix.EEXIST):
		return nil, ErrExists
	case err != nil:
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (d *unixDir) OpenFile(name string) (*os.File, error) {
	f, err := openRegular(d.fd, name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// openRegular opens name below dirfd without following links and requires a
// regular file. O_NONBLOCK keeps a FIFO from blocking the open.
func openRegular(dirfd int, name string, access int) (*os.File, error) {
	fd, err := openat(dirfd, name, access|fileFlags, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, fs.ErrNotExist
	case isLinkErr(err):
		return nil, fmt.Errorf("%q is a link; Yggdrasil never follows links: %w", name, ErrUnsafePath)
	case err != nil:
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%q is not a plain file: %w", name, ErrUnsafePath)
	}
	return f, nil
}

type unixWalker struct{}

// OpenParent walks from "/" one component at a time with O_NOFOLLOW, so no
// ancestor may be a symlink.
func (unixWalker) OpenParent(parent string) (fsDir, error) {
	if !filepath.IsAbs(parent) {
		return nil, fmt.Errorf("%q is not a full path: %w", parent, ErrUnsafePath)
	}
	if strings.ContainsRune(parent, 0) {
		return nil, fmt.Errorf("the path contains a NUL byte: %w", ErrUnsafePath)
	}
	fd, err := openat(unix.AT_FDCWD, "/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open the file system root: %w", err)
	}
	for _, c := range strings.Split(filepath.Clean(parent), string(filepath.Separator)) {
		if c == "" {
			continue
		}
		next, err := openat(fd, c, dirFlags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, wrapDirErr(err, c)
		}
		fd = next
	}
	return newUnixDir(fd)
}

// --- IgnoreWriter ---

// NewIgnoreWriter returns the real IgnoreWriter of this platform.
func NewIgnoreWriter() IgnoreWriter { return ignoreWriter{ops: unixIgnore{}} }

type unixIgnore struct{}

type unixIgnoreHandle struct {
	f    *os.File
	root *os.File
}

func (h *unixIgnoreHandle) File() *os.File { return h.f }

func (h *unixIgnoreHandle) Close() {
	_ = h.f.Close() // closing releases the advisory lock
	_ = h.root.Close()
}

func (h *unixIgnoreHandle) SameAsPath() (bool, error) {
	var a, b unix.Stat_t
	if err := unix.Fstat(int(h.f.Fd()), &a); err != nil {
		return false, err
	}
	if err := unix.Fstatat(int(h.root.Fd()), ".gitignore", &b, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	return a.Dev == b.Dev && a.Ino == b.Ino, nil
}

func openRoot(root string) (*os.File, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%q is not a full path: %w", root, ErrUnsafePath)
	}
	// The checkout root is the tracked folder the caller already vetted; only
	// the .gitignore inside it must not be a link.
	fd, err := openat(unix.AT_FDCWD, root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open the repo folder %q: %w", root, err)
	}
	return os.NewFile(uintptr(fd), root), nil
}

func (unixIgnore) OpenIgnore(root string, write bool) (ignoreHandle, bool, error) {
	r, err := openRoot(root)
	if err != nil {
		return nil, false, err
	}
	access := unix.O_RDONLY
	if write {
		access = unix.O_RDWR | unix.O_APPEND // append: can never overwrite or truncate
	}
	f, err := openRegular(int(r.Fd()), ".gitignore", access)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		_ = r.Close()
		return nil, false, nil
	case err != nil:
		_ = r.Close()
		return nil, false, err
	}
	if write {
		err := retryEINTR(func() error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) })
		if err != nil {
			_ = f.Close()
			_ = r.Close()
			if errors.Is(err, unix.EWOULDBLOCK) {
				return nil, false, fmt.Errorf(".gitignore is locked by another program; close it and retry: %w", ErrLocked)
			}
			return nil, false, fmt.Errorf("lock the .gitignore: %w", err)
		}
	}
	return &unixIgnoreHandle{f: f, root: r}, true, nil
}

func (unixIgnore) CreateIgnore(root string, content []byte) error {
	r, err := openRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	fd, err := openat(int(r.Fd()), ".gitignore", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|fileFlags, 0o644)
	switch {
	case errors.Is(err, unix.EEXIST):
		return ErrExists
	case err != nil:
		return fmt.Errorf("create the .gitignore: %w", err)
	}
	f := os.NewFile(uintptr(fd), ".gitignore")
	_, werr := f.Write(content)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("write the new .gitignore (the partial file is left in place): %w", err)
	}
	return nil
}
