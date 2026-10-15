//go:build windows

package platform

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Explorer COM launch (docs/spikes.md B2): ask the desktop's Explorer to run a
// program through IShellDispatch2.ShellExecute, so Explorer is its parent and
// it is in none of our jobs. This is the documented way to start a program
// "as the shell" (Raymond Chen, "How can I launch an unelevated process from
// my elevated process"); it is not parent-process spoofing. No COM package is
// vendored, so the few interfaces involved are called through their vtables.

var (
	modole32     = windows.NewLazySystemDLL("ole32.dll")
	modoleaut32  = windows.NewLazySystemDLL("oleaut32.dll")
	procCoCreate = modole32.NewProc("CoCreateInstance")
	procAllocStr = modoleaut32.NewProc("SysAllocString")
	procFreeStr  = modoleaut32.NewProc("SysFreeString")

	clsidShellWindows = mustGUID("{9BA05972-F6A8-11CF-A442-00A0C90A8F39}")
	iidShellWindows   = mustGUID("{85CB6900-4D95-11CF-960C-0080C7F4EE85}")
	iidServiceProv    = mustGUID("{6D5140C1-7436-11CE-8034-00AA006009FA}")
	sidTopLevelBrowse = mustGUID("{4C96BE40-915C-11CF-99D3-00AA004AE837}")
	iidShellBrowser   = mustGUID("{000214E2-0000-0000-C000-000000000046}")
	iidDispatch       = mustGUID("{00020400-0000-0000-C000-000000000046}")
	iidNull           windows.GUID
)

const (
	clsctxLocalServer = 0x4
	swcDesktop        = 0x8 // SWC_DESKTOP
	swfoNeedDispatch  = 0x1 // SWFO_NEEDDISPATCH
	svgioBackground   = 0   // SVGIO_BACKGROUND
	vtEmpty           = 0
	vtI4              = 3
	vtBstr            = 8
	vtDispatch        = 9
	dispatchMethod    = 1
	dispatchPropGet   = 2
	rpcEChangedMode   = syscall.Errno(0x80010106)

	// vtable slots
	slotRelease        = 2
	slotQueryInterface = 0
	slotGetIDsOfNames  = 5
	slotInvoke         = 6
	slotFindWindowSW   = 15 // IShellWindows
	slotQueryService   = 3  // IServiceProvider
	slotActiveView     = 15 // IShellBrowser.QueryActiveShellView
	slotGetItemObject  = 15 // IShellView
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err) // constant input
	}
	return g
}

// iface is the memory layout of any COM interface pointer.
type iface struct{ vtbl *[32]uintptr }

func (o *iface) call(slot int, args ...uintptr) uintptr {
	all := append([]uintptr{ptr(o)}, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[slot], all...)
	return r
}

func (o *iface) release() { o.call(slotRelease) }

// failed reports a failing HRESULT (high bit set).
func failed(hr uintptr) bool { return hr&0x80000000 != 0 }

func ptr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) } //nolint:gosec // raw COM call

// variant is a 64-bit VARIANT: type tag, padding, 16-byte union.
type variant struct {
	vt  uint16
	_   [3]uint16
	val [2]uintptr
}

type dispParams struct {
	args  *variant
	_     *int32 // named arguments, unused
	cArgs uint32
	_     uint32 // number of named arguments
}

// free releases the BSTR of a bstrVariant.
func (v variant) free() { _, _, _ = procFreeStr.Call(v.val[0]) }

func bstrVariant(s string) (variant, error) {
	w, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return variant{}, err
	}
	b, _, _ := procAllocStr.Call(ptr(w))
	if b == 0 {
		return variant{}, errors.New("out of memory")
	}
	return variant{vt: vtBstr, val: [2]uintptr{b}}, nil
}

// invoke calls a method or property of an IDispatch by name. args are in
// natural order.
func invoke(d *iface, name string, flags uintptr, args ...variant) (variant, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return variant{}, err
	}
	var id uint32
	if hr := d.call(slotGetIDsOfNames, ptr(&iidNull), ptr(&n), 1, 0, ptr(&id)); failed(hr) {
		return variant{}, fmt.Errorf("no member %s (HRESULT 0x%x)", name, hr)
	}
	// Invoke wants the arguments in reverse order.
	rev := make([]variant, len(args))
	for i, a := range args {
		rev[len(args)-1-i] = a
	}
	dp := dispParams{cArgs: uint32(len(rev))} //nolint:gosec // at most 5 arguments
	if len(rev) > 0 {
		dp.args = &rev[0]
	}
	var res variant
	if hr := d.call(slotInvoke, uintptr(id), ptr(&iidNull), 0, flags, ptr(&dp), ptr(&res), 0, 0); failed(hr) {
		return variant{}, fmt.Errorf("%s failed (HRESULT 0x%x)", name, hr)
	}
	return res, nil
}

// shellExecute asks Explorer to run file with args (a ready command-line
// string) in dir (may be empty).
func shellExecute(file, args, dir string) error {
	errc := make(chan error, 1)
	go func() {
		// COM objects belong to the thread that created them.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		errc <- shellExecuteSTA(file, args, dir)
	}()
	return <-errc
}

func shellExecuteSTA(file, args, dir string) error {
	switch err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); {
	case err == nil || errors.Is(err, syscall.Errno(1)): // S_OK, S_FALSE
		defer windows.CoUninitialize()
	case errors.Is(err, rpcEChangedMode): // thread already initialised another way
	default:
		return fmt.Errorf("initialise COM: %w", err)
	}
	const hint = "could not ask Windows Explorer to open the program outside Yggdrasil"
	app, err := explorerApplication()
	if err != nil {
		return fmt.Errorf("%s: %w", hint, err)
	}
	defer app.release()

	vFile, err := bstrVariant(file)
	if err != nil {
		return err
	}
	defer vFile.free()
	vArgs, err := bstrVariant(args)
	if err != nil {
		return err
	}
	defer vArgs.free()
	vDir := variant{vt: vtEmpty}
	if dir != "" {
		if vDir, err = bstrVariant(dir); err != nil {
			return err
		}
		defer vDir.free()
	}
	// ShellExecute(File, vArguments, vDirectory, vOperation, vShow)
	if _, err := invoke(app, "ShellExecute", dispatchMethod,
		vFile, vArgs, vDir, variant{vt: vtEmpty}, variant{vt: vtI4, val: [2]uintptr{1}}); err != nil {
		return fmt.Errorf("%s: %w", hint, err)
	}
	return nil
}

// explorerApplication returns the Shell.Application object that lives in
// Explorer's own process: desktop shell view -> its Application property.
func explorerApplication() (*iface, error) {
	var sw *iface
	hr, _, _ := procCoCreate.Call(ptr(&clsidShellWindows), 0, clsctxLocalServer, ptr(&iidShellWindows), ptr(&sw))
	if failed(hr) {
		return nil, fmt.Errorf("windows explorer is not available (HRESULT 0x%x)", hr)
	}
	defer sw.release()

	vLoc := variant{vt: vtI4} // CSIDL_DESKTOP = 0
	vRoot := variant{vt: vtEmpty}
	var hwnd int32
	var desktop *iface
	if hr := sw.call(slotFindWindowSW, ptr(&vLoc), ptr(&vRoot), swcDesktop, ptr(&hwnd), swfoNeedDispatch, ptr(&desktop)); failed(hr) || hr != 0 {
		return nil, fmt.Errorf("no desktop shell window (HRESULT 0x%x)", hr)
	}
	defer desktop.release()

	var sp *iface
	if hr := desktop.call(slotQueryInterface, ptr(&iidServiceProv), ptr(&sp)); failed(hr) {
		return nil, fmt.Errorf("shell window has no service provider (HRESULT 0x%x)", hr)
	}
	defer sp.release()
	var sb *iface
	if hr := sp.call(slotQueryService, ptr(&sidTopLevelBrowse), ptr(&iidShellBrowser), ptr(&sb)); failed(hr) {
		return nil, fmt.Errorf("no shell browser (HRESULT 0x%x)", hr)
	}
	defer sb.release()
	var sv *iface
	if hr := sb.call(slotActiveView, ptr(&sv)); failed(hr) {
		return nil, fmt.Errorf("no shell view (HRESULT 0x%x)", hr)
	}
	defer sv.release()
	var bg *iface
	if hr := sv.call(slotGetItemObject, svgioBackground, ptr(&iidDispatch), ptr(&bg)); failed(hr) {
		return nil, fmt.Errorf("no shell folder view (HRESULT 0x%x)", hr)
	}
	defer bg.release()

	res, err := invoke(bg, "Application", dispatchPropGet)
	if err != nil {
		return nil, err
	}
	if res.vt != vtDispatch || res.val[0] == 0 {
		return nil, errors.New("shell folder view returned no Application")
	}
	return *(**iface)(unsafe.Pointer(&res.val[0])), nil //nolint:gosec // reads the IDispatch pointer out of the VARIANT union
}
