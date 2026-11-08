//go:build !windows

package platform

import "errors"

// ListeningPorts is only implemented on Windows (DV-02); ygg's Unix support is
// minimal (docs/AGENTS.md 5.2).
func ListeningPorts(int) ([]int, error) {
	return nil, errors.New("platform: port detection is not supported on this OS")
}
