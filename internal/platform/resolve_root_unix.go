//go:build !windows

package platform

import "path/filepath"

// canonicalExisting resolves an existing path: symlinks followed.
func canonicalExisting(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
