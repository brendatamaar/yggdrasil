//go:build windows

package platform

import "testing"

func TestMachineReadsFreeMemoryOnWindows(t *testing.T) {
	if (Machine{}).FreeMemMB() <= 0 {
		t.Fatal("free memory was not read")
	}
}
