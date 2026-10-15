//go:build windows

package platform

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	extendedPrefix = `\\?\`
	uncPrefix      = `\\?\UNC\`
	// longPathThreshold is below MAX_PATH (260) minus the 8.3 slack
	// GetLongPathNameW needs, so only deep paths get the \\?\ prefix.
	longPathThreshold = 240
)

// LongPath makes path absolute and expands 8.3 short names with
// GetLongPathNameW (the permission check of an agent CLI fails on short TEMP
// paths, docs/spikes.md B4). The path must exist. A lower-case drive letter
// becomes upper case.
func LongPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make %s absolute: %w", path, err)
	}
	p, prefixed := abs, false
	if len(p) >= longPathThreshold && !strings.HasPrefix(p, extendedPrefix) {
		if rest, ok := strings.CutPrefix(p, `\\`); ok {
			p = uncPrefix + rest
		} else {
			p = extendedPrefix + p
		}
		prefixed = true
	}
	src, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", fmt.Errorf("expand %s: %w", path, err)
	}
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetLongPathName(src, &buf[0], uint32(len(buf))) //nolint:gosec // len(buf) is small
		if err != nil {
			return "", fmt.Errorf("expand %s (does the folder exist?): %w", path, err)
		}
		if int(n) > len(buf) {
			buf = make([]uint16, n)
			continue
		}
		p = windows.UTF16ToString(buf[:n])
		break
	}
	if prefixed {
		if rest, ok := strings.CutPrefix(p, uncPrefix); ok {
			p = `\\` + rest
		} else {
			p = strings.TrimPrefix(p, extendedPrefix)
		}
	}
	if len(p) >= 2 && p[1] == ':' {
		p = strings.ToUpper(p[:1]) + p[1:]
	}
	return p, nil
}
