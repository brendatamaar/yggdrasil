package platform

import (
	"slices"
	"testing"
)

func TestPortsInRange(t *testing.T) {
	got := portsInRange([]int{9, 20005, 20001, 20005, 30000, 29999, 20000}, 20000, 29999)
	if !slices.Equal(got, []int{20000, 20001, 20005, 29999}) {
		t.Fatalf("got %v", got)
	}
	if got := portsInRange(nil, 1, 10); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestBusyPortsRejectsBadRange(t *testing.T) {
	for _, r := range [][2]int{{0, 10}, {10, 5}, {1, 70000}} {
		if _, err := BusyPorts(r[0], r[1]); err == nil {
			t.Errorf("%v accepted", r)
		}
	}
}
