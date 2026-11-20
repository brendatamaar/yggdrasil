//go:build windows

package platform

import (
	"errors"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// A missing COM interface must report a failing HRESULT and release must do
// nothing, never crash (the race run once panicked on a nil interface).
func TestIfaceNilSafe(t *testing.T) {
	var nilIface *iface
	if hr := nilIface.call(slotFindWindowSW); !failed(hr) || hr != ePointer {
		t.Errorf("call on a nil interface = 0x%x, want E_POINTER", hr)
	}
	nilIface.release() // must not panic

	noVtbl := &iface{}
	if hr := noVtbl.call(slotRelease); hr != ePointer {
		t.Errorf("call without a vtable = 0x%x, want E_POINTER", hr)
	}
	noVtbl.release()

	emptyVtbl := &iface{vtbl: new([32]uintptr)}
	if hr := emptyVtbl.call(slotInvoke); hr != ePointer {
		t.Errorf("call on an empty slot = 0x%x, want E_POINTER", hr)
	}
	emptyVtbl.release()

	if _, err := invoke(nil, "ShellExecute", dispatchMethod); err == nil {
		t.Error("invoke on a nil object succeeded")
	}
}

// explorerApplication either returns a usable object or the plain
// "explorer is not available" error; it never panics, with or without a desktop.
func TestExplorerApplicationNeverPanics(t *testing.T) {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		errc <- func() error {
			if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil {
				defer windows.CoUninitialize()
			}
			app, err := explorerApplication()
			if err != nil {
				return err
			}
			app.release()
			return nil
		}()
	}()
	if err := <-errc; err != nil && !errors.Is(err, errExplorerUnavailable) {
		// Some other plain error (e.g. a missing member) is still not a panic.
		t.Logf("explorerApplication: %v", err)
	}
}
