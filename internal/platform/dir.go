package platform

import (
	"errors"
	"fmt"
	"os"
)

// ErrDirMissing is returned by Start and StartPTY when Spec.Dir does not exist
// or is not a folder (for example a worktree that was removed). Callers
// classify it as "dir_missing" (docs/design/builds.md).
var ErrDirMissing = errors.New("platform: the working folder does not exist")

// prepareDir checks the working directory of a process and returns it in the
// form to start the process in: a supported path (CheckPath), existing, with
// 8.3 names expanded (LongPath). An empty dir is returned as is.
func prepareDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	if err := CheckPath(dir); err != nil {
		return "", err
	}
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%w: %s. The folder may have been moved or removed; check that it still exists", ErrDirMissing, dir)
	case err != nil:
		return "", fmt.Errorf("check the working folder %s: %w", dir, err)
	case !fi.IsDir():
		return "", fmt.Errorf("%w: %s is a file, not a folder", ErrDirMissing, dir)
	}
	return LongPath(dir)
}
