//go:build linux

package platform

import (
	"errors"
	"os"
)

// allListeningPorts lists every TCP port in LISTEN state, IPv4 and IPv6, from
// /proc/net/tcp and tcp6. IPv6 may be disabled, so one missing table is fine;
// both missing is an error.
func allListeningPorts() ([]int, error) {
	var ports []int
	read := 0
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(file) //nolint:gosec // fixed /proc paths
		if err != nil {
			continue
		}
		read++
		for _, port := range listeningSockets(f) {
			ports = append(ports, port)
		}
		_ = f.Close()
	}
	if read == 0 {
		return nil, errors.New("platform: cannot read the TCP listener table")
	}
	return ports, nil
}
