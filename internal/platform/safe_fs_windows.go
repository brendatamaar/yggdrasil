//go:build windows

package platform

// Windows primitives of the safe filesystem capabilities. Everything is
// handle-relative through NtCreateFile (a RootDirectory handle plus one name),
// opened with FILE_OPEN_REPARSE_POINT so a symlink, junction or mount point is
// never traversed: the handle's attributes are then checked and a reparse
// point is refused. Creates use FILE_CREATE (exclusive). Identity is the volume
// serial number and file index. Names can be long: NT paths have no MAX_PATH
// limit. Nothing here deletes, truncates or overwrites.
//
// This file has been compiled for Windows (GOOS=windows go vet) but the
// behaviour is verified only by the contract tests that run on Windows CI and
// by the manual checklist in the N1 report.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NewSafeInitFS returns the real SafeInitFS of this platform.
func NewSafeInitFS() SafeInitFS { return safeInitFS{w: winWalker{}} }

const (
	shareAll   = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	dirAccess  = windows.FILE_LIST_DIRECTORY | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE
	optSync    = windows.FILE_SYNCHRONOUS_IO_NONALERT
	optNoFollo = windows.FILE_OPEN_REPARSE_POINT
)

var errNotDir = errors.New("not a folder")

// ntOpen opens name relative to root (or an absolute \??\ path when root is
// zero) with NtCreateFile. The result is a synchronous handle.
func ntOpen(root windows.Handle, name string, access, share, disposition, options uint32) (windows.Handle, error) {
	us, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, fmt.Errorf("the name %q is too long: %w", name, ErrUnsafePath)
	}
	oa := &windows.OBJECT_ATTRIBUTES{RootDirectory: root, ObjectName: us, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	st := windows.NtCreateFile(&h, access, oa, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL, share, disposition, options, 0, 0)
	_ = us // keep the name buffer reachable until the call returned
	if st != nil {
		return 0, ntErr(st)
	}
	return h, nil
}

// ntErr maps an NTSTATUS to the package's sentinels.
func ntErr(err error) error {
	st, ok := err.(windows.NTStatus) //nolint:errorlint // NtCreateFile returns the bare status
	if !ok {
		return err
	}
	switch st {
	case windows.STATUS_OBJECT_NAME_COLLISION:
		return ErrExists
	case windows.STATUS_OBJECT_NAME_NOT_FOUND, windows.STATUS_OBJECT_PATH_NOT_FOUND:
		return fs.ErrNotExist
	case windows.STATUS_SHARING_VIOLATION, windows.STATUS_DELETE_PENDING:
		return ErrLocked
	case windows.STATUS_NOT_A_DIRECTORY:
		return errNotDir
	case windows.STATUS_FILE_IS_A_DIRECTORY:
		return fmt.Errorf("a folder is where a file was expected: %w", ErrUnsafePath)
	case windows.STATUS_REPARSE_POINT_ENCOUNTERED, windows.STATUS_STOPPED_ON_SYMLINK:
		return fmt.Errorf("a link or reparse point was found: %w", ErrUnsafePath)
	case windows.STATUS_ACCESS_DENIED:
		return fs.ErrPermission
	}
	return fmt.Errorf("%w (NTSTATUS %#x)", st.Errno(), uint32(st))
}

type winInfo struct {
	attrs uint32
	id    DirIdentity
	size  int64
}

func infoOf(h windows.Handle) (winInfo, error) {
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return winInfo{}, fmt.Errorf("read file information: %w", err)
	}
	return winInfo{
		attrs: fi.FileAttributes,
		id:    DirIdentity(fmt.Sprintf("win:%x:%x%08x", fi.VolumeSerialNumber, fi.FileIndexHigh, fi.FileIndexLow)),
		size:  int64(fi.FileSizeHigh)<<32 | int64(fi.FileSizeLow),
	}, nil
}

func (i winInfo) reparse() bool { return i.attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 }
func (i winInfo) isDir() bool   { return i.attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 }

type winDirHandle struct {
	h  windows.Handle
	id DirIdentity
}

func (d *winDirHandle) ID() DirIdentity { return d.id }
func (d *winDirHandle) Close()          { _ = windows.CloseHandle(d.h) }

// wrapDirHandle checks that h is a plain folder (not a reparse point) and
// records its identity. It closes h on failure.
func wrapDirHandle(h windows.Handle, name string) (*winDirHandle, error) {
	info, err := infoOf(h)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	if info.reparse() || !info.isDir() {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("%q is a link, junction or not a folder; Yggdrasil never writes through links: %w", name, ErrUnsafePath)
	}
	return &winDirHandle{h: h, id: info.id}, nil
}

func (d *winDirHandle) OpenDir(leaf string) (fsDir, error) {
	h, err := ntOpen(d.h, leaf, dirAccess, shareAll, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|optNoFollo|optSync)
	if err != nil {
		if errors.Is(err, errNotDir) {
			return nil, fmt.Errorf("%q is not a folder: %w", leaf, ErrUnsafePath)
		}
		return nil, err
	}
	return wrapDirHandle(h, leaf)
}

func (d *winDirHandle) Mkdir(leaf string) (fsDir, error) {
	h, err := ntOpen(d.h, leaf, dirAccess, shareAll, windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE|optNoFollo|optSync)
	if err != nil {
		return nil, err
	}
	return wrapDirHandle(h, leaf)
}

func (d *winDirHandle) CreateFile(name string) (*os.File, error) {
	h, err := ntOpen(d.h, name,
		windows.FILE_WRITE_DATA|windows.FILE_WRITE_ATTRIBUTES|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ, windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|optNoFollo|optSync)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

func (d *winDirHandle) OpenFile(name string) (*os.File, error) {
	return openWinFile(d.h, name, windows.FILE_READ_DATA|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, shareAll)
}

// openWinFile opens an existing plain file below dir without following
// reparse points and checks the handle's attributes.
func openWinFile(dir windows.Handle, name string, access, share uint32) (*os.File, error) {
	h, err := ntOpen(dir, name, access, share, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|optNoFollo|optSync)
	if err != nil {
		return nil, err
	}
	info, err := infoOf(h)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	if info.reparse() || info.isDir() {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("%q is a link or not a plain file: %w", name, ErrUnsafePath)
	}
	return os.NewFile(uintptr(h), name), nil
}

type winWalker struct{}

// finalPath is the on-disk name of the folder behind h, without the \\?\
// prefix. Links, subst drives and 8.3 aliases all show up as a different path.
func finalPath(h windows.Handle) (string, error) {
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0) //nolint:gosec // len(buf) is small
		if err != nil {
			return "", fmt.Errorf("read the name of the folder: %w", err)
		}
		if int(n) > len(buf) {
			buf = make([]uint16, n)
			continue
		}
		out := windows.UTF16ToString(buf[:n])
		if rest, ok := strings.CutPrefix(out, `\\?\UNC\`); ok {
			return `\\` + rest, nil
		}
		return strings.TrimPrefix(out, `\\?\`), nil
	}
}

// OpenParent walks the drive-letter path below its root one component at a
// time with FILE_OPEN_REPARSE_POINT, refusing every reparse point, and finally
// requires the handle's own name to equal the requested path (which catches
// 8.3 aliases and subst drives).
func (winWalker) OpenParent(parent string) (fsDir, error) {
	d, err := parseWindowsDir(parent, true)
	if err != nil {
		return nil, err
	}
	h, err := ntOpen(0, `\??\`+d.Drive+`:\`, dirAccess, shareAll, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|optSync)
	if err != nil {
		return nil, fmt.Errorf("open the drive %s: %w", d.Drive, err)
	}
	cur, err := wrapDirHandle(h, d.Drive+`:\`)
	if err != nil {
		return nil, err
	}
	for _, c := range d.Comps {
		next, err := cur.OpenDir(c)
		cur.Close()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("the folder %q does not exist: %w", c, fs.ErrNotExist)
			}
			return nil, err
		}
		cur = next.(*winDirHandle) //nolint:forcetypeassert // OpenDir of winDirHandle returns winDirHandle
	}
	got, err := finalPath(cur.h)
	if err != nil {
		cur.Close()
		return nil, err
	}
	if !strings.EqualFold(got, d.String()) {
		cur.Close()
		return nil, fmt.Errorf("%q is not the real name of the folder (it is %q): a link, short name or substituted drive is on the way: %w", parent, got, ErrUnsafePath)
	}
	return cur, nil
}

// --- IgnoreWriter ---

// NewIgnoreWriter returns the real IgnoreWriter of this platform.
func NewIgnoreWriter() IgnoreWriter { return ignoreWriter{ops: winIgnore{}} }

type winIgnore struct{}

type winIgnoreHandle struct {
	f    *os.File
	root windows.Handle
	id   DirIdentity
}

func (h *winIgnoreHandle) File() *os.File { return h.f }

func (h *winIgnoreHandle) Close() {
	_ = h.f.Close()
	_ = windows.CloseHandle(h.root)
}

func (h *winIgnoreHandle) SameAsPath() (bool, error) {
	again, err := openWinFile(h.root, ".gitignore", windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, shareAll)
	if err != nil {
		return false, err
	}
	defer func() { _ = again.Close() }()
	info, err := infoOf(windows.Handle(again.Fd()))
	if err != nil {
		return false, err
	}
	return info.id == h.id, nil
}

// openIgnoreRoot opens the checkout folder. The root is the tracked folder the
// caller already vetted (and canonicalized), so it is opened as given; only
// the .gitignore inside must not be a link.
func openIgnoreRoot(root string) (windows.Handle, error) {
	d, err := parseWindowsDir(root, false)
	if err != nil {
		return 0, err
	}
	h, err := ntOpen(0, `\??\`+d.String(), dirAccess, shareAll, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|optSync)
	if err != nil {
		return 0, fmt.Errorf("open the repo folder %q: %w", root, err)
	}
	return h, nil
}

func (winIgnore) OpenIgnore(root string, write bool) (ignoreHandle, bool, error) {
	rh, err := openIgnoreRoot(root)
	if err != nil {
		return nil, false, err
	}
	access, share := uint32(windows.FILE_READ_DATA|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE), uint32(shareAll)
	if write {
		// FILE_APPEND_DATA without FILE_WRITE_DATA: every write lands at the
		// end, so nothing can be overwritten or truncated. Sharing is read
		// only: other writers and deleters are kept out while we hold it.
		access |= windows.FILE_APPEND_DATA
		share = windows.FILE_SHARE_READ
	}
	f, err := openWinFile(rh, ".gitignore", access, share)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		_ = windows.CloseHandle(rh)
		return nil, false, nil
	case errors.Is(err, ErrLocked):
		_ = windows.CloseHandle(rh)
		return nil, false, fmt.Errorf(".gitignore is open in another program; close it and retry: %w", ErrLocked)
	case err != nil:
		_ = windows.CloseHandle(rh)
		return nil, false, err
	}
	info, err := infoOf(windows.Handle(f.Fd()))
	if err != nil {
		_ = f.Close()
		_ = windows.CloseHandle(rh)
		return nil, false, err
	}
	return &winIgnoreHandle{f: f, root: rh, id: info.id}, true, nil
}

func (winIgnore) CreateIgnore(root string, content []byte) error {
	rh, err := openIgnoreRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(rh) }()
	h, err := ntOpen(rh, ".gitignore",
		windows.FILE_WRITE_DATA|windows.FILE_WRITE_ATTRIBUTES|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ, windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|optNoFollo|optSync)
	if err != nil {
		if errors.Is(err, ErrExists) {
			return ErrExists
		}
		return fmt.Errorf("create the .gitignore: %w", err)
	}
	f := os.NewFile(uintptr(h), ".gitignore")
	_, werr := f.Write(content)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("write the new .gitignore (the partial file is left in place): %w", err)
	}
	return nil
}
