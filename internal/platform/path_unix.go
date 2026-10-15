//go:build !windows

package platform

import (
	"fmt"
	"path/filepath"
)

// LongPath makes path absolute. Unix has no 8.3 names.
func LongPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make %s absolute: %w", path, err)
	}
	return abs, nil
}
