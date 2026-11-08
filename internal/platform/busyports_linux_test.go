//go:build linux

package platform

import (
	"slices"
	"testing"
)

// A listener on the wildcard address counts as busy, and so does one on
// loopback only. A bind probe on 127.0.0.1 can miss a wildcard listener.
func TestBusyPortsSeesWildcardAndLoopbackListeners(t *testing.T) {
	wild := listen(t, "tcp", ":0")
	loop := listen(t, "tcp4", "127.0.0.1:0")
	if wild == 0 || loop == 0 {
		t.Skip("cannot listen")
	}
	lo, hi := min(wild, loop), max(wild, loop)
	got, err := BusyPorts(lo, hi)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, wild) || !slices.Contains(got, loop) {
		t.Fatalf("got %v, want %d and %d", got, wild, loop)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("not sorted: %v", got)
	}
}
