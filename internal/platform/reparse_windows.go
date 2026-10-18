//go:build windows

package platform

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// IsReparsePoint reports whether path is a symlink, junction or other reparse
// point. The folder scan and the folder browser never follow one, so a loop
// or a link out of the tracked folder can't be walked. It looks at the entry
// itself, not at its target. A path that can't be read is an error.
func IsReparsePoint(path string) (bool, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, fmt.Errorf("check %s: %w", path, err)
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return false, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

// ListRoots returns the roots the folder browser starts from: the local
// drives that are ready, such as "C:\".
func ListRoots() ([]string, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, fmt.Errorf("list drives: %w", err)
	}
	var roots []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		switch p, err := windows.UTF16PtrFromString(root); {
		case err != nil:
			continue
		default:
			if t := windows.GetDriveType(p); t == windows.DRIVE_UNKNOWN || t == windows.DRIVE_NO_ROOT_DIR {
				continue
			}
		}
		roots = append(roots, root)
	}
	return roots, nil
}
