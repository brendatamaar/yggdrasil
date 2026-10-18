//go:build windows

package platform

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// PathsCaseInsensitive reports whether two spellings of a path that differ
// only in case are the same folder.
const PathsCaseInsensitive = true

// CanonicalPath returns the one spelling of an existing folder or file that
// the disk itself reports: the on-disk case of every segment, long names, the
// drive letter in upper case, without the \\?\ prefix. Links on the way are
// resolved, so callers that must refuse links check for them first.
func CanonicalPath(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", fmt.Errorf("open %s (does the folder exist?): %w", path, err)
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0) //nolint:gosec // len(buf) is small
		if err != nil {
			return "", fmt.Errorf("read the name of %s: %w", path, err)
		}
		if int(n) > len(buf) {
			buf = make([]uint16, n)
			continue
		}
		out := windows.UTF16ToString(buf[:n])
		if rest, ok := strings.CutPrefix(out, `\\?\UNC\`); ok {
			out = `\\` + rest
		} else {
			out = strings.TrimPrefix(out, `\\?\`)
		}
		if len(out) >= 2 && out[1] == ':' {
			out = strings.ToUpper(out[:1]) + out[1:]
		}
		return out, nil
	}
}
