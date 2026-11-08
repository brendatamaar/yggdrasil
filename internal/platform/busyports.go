package platform

import (
	"errors"
	"slices"
)

// BusyPorts returns the sorted TCP ports in lo..hi that some process already
// listens on, on any address (a port bound to the wildcard address, or to
// another interface, counts as busy). It reads the operating system's
// listener table once; it does not try to bind any port (WE-02). It fails on
// operating systems without a listener table implementation.
func BusyPorts(lo, hi int) ([]int, error) {
	if lo < 1 || hi > 65535 || lo > hi {
		return nil, errors.New("platform: invalid port range")
	}
	all, err := allListeningPorts()
	if err != nil {
		return nil, err
	}
	return portsInRange(all, lo, hi), nil
}

// portsInRange returns the distinct ports of all that lie in lo..hi, sorted.
func portsInRange(all []int, lo, hi int) []int {
	var out []int
	for _, p := range all {
		if p >= lo && p <= hi {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
