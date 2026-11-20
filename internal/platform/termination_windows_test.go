//go:build windows

package platform

import (
	"testing"

	"golang.org/x/sys/windows"
)

func waitTerminationExit(t *testing.T, p Process, permitExit func()) {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(p.Pid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(h) }() // test helper: nothing to do on failure
	permitExit()
	result, err := windows.WaitForSingleObject(h, 10000)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("helper did not exit: %d %v", result, err)
	}
}
