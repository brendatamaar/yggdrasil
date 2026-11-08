//go:build !windows && !linux

package platform

import "errors"

// ListeningPorts is implemented on Windows and Linux only (DV-02); ygg's other
// Unix support is minimal (docs/AGENTS.md 5.2).
func ListeningPorts(int) ([]int, error) {
	return nil, errors.New("platform: port detection is not supported on this OS")
}
