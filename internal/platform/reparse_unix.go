//go:build !windows

package platform

import (
	"fmt"
	"os"
)

// IsReparsePoint reports whether path is a symlink (the Unix counterpart of a
// Windows reparse point). It looks at the entry itself, not at its target.
func IsReparsePoint(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("check %s: %w", path, err)
	}
	return fi.Mode()&os.ModeSymlink != 0, nil
}

// ListRoots returns the roots the folder browser starts from: "/".
func ListRoots() ([]string, error) { return []string{"/"}, nil }
