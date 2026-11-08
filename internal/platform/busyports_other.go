//go:build !windows && !linux

package platform

import "errors"

// allListeningPorts is implemented on Windows and Linux only; ygg's other
// Unix support is minimal (docs/AGENTS.md 5.2).
func allListeningPorts() ([]int, error) {
	return nil, errors.New("platform: port detection is not supported on this OS")
}
