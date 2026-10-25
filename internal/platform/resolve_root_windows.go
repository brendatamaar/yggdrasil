//go:build windows

package platform

import "strings"

// canonicalExisting resolves an existing path on disk. Deep paths get the
// \\?\ prefix so MAX_PATH does not apply.
func canonicalExisting(path string) (string, error) {
	p := path
	if len(p) >= longPathThreshold && !strings.HasPrefix(p, extendedPrefix) {
		if rest, ok := strings.CutPrefix(p, `\\`); ok {
			p = uncPrefix + rest
		} else {
			p = extendedPrefix + p
		}
	}
	return CanonicalPath(p)
}
