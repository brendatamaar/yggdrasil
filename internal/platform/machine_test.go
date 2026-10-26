package platform

import "testing"

func TestMachineReportsRoom(t *testing.T) {
	var m Machine
	if m.CPUs() < 1 {
		t.Fatalf("CPUs = %d", m.CPUs())
	}
	if m.FreeMemMB() < 0 {
		t.Fatalf("FreeMemMB = %d", m.FreeMemMB())
	}
}
