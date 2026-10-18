//go:build !windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// PathsCaseInsensitive reports whether two spellings of a path that differ
// only in case are the same folder. Unix file systems are case-sensitive here.
const PathsCaseInsensitive = false

// CanonicalPath returns the absolute, cleaned path of an existing folder or
// file. Unix has one spelling per path.
func CanonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make %s absolute: %w", path, err)
	}
	if _, err := os.Lstat(abs); err != nil {
		return "", fmt.Errorf("read %s (does the folder exist?): %w", path, err)
	}
	return abs, nil
}
