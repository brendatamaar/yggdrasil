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

// ePointer is E_POINTER, what a call on a missing interface reports.
const ePointer = 0x80004003

// errExplorerUnavailable means there is no Explorer shell to talk to (a
// headless session, a service, a locked-down runner). Callers and tests treat
// it as "cannot launch through Explorer here", not as a bug.
var errExplorerUnavailable = errors.New("windows explorer is not available")

// iface is the memory layout of any COM interface pointer.
type iface struct{ vtbl *[32]uintptr }

// call invokes a vtable slot. A nil interface (or one without a vtable, or an
// empty slot) reports E_POINTER instead of crashing.
func (o *iface) call(slot int, args ...uintptr) uintptr {
	if o == nil || o.vtbl == nil || slot < 0 || slot >= len(o.vtbl) || o.vtbl[slot] == 0 {
		return ePointer
	}
	all := append([]uintptr{ptr(o)}, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[slot], all...)
	return r
}

// release drops one reference; it does nothing on a nil interface.
func (o *iface) release() {
	if o != nil {
		o.call(slotRelease)
	}
}

// heapNew returns a zeroed T that lives on the heap and stays alive and in place
// until pin.Unpin. COM writes its out-values through raw addresses (ptr turns a
// pointer into a uintptr), and Go does not track such an address: a local the
// compiler keeps on the stack moves when the stack grows, so the callee would
// write into the old copy and the local would stay zero (a nil interface after
// a successful HRESULT). The race build grows stacks more often and showed it.
// Pinning forces the object to the heap, where it never moves.
func heapNew[T any](pin *runtime.Pinner) *T {
	p := new(T)
	pin.Pin(p)
	return p
}

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
	if d == nil {
		return variant{}, fmt.Errorf("%s: no object to call (HRESULT 0x%x)", name, ePointer)
	}
	w, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return variant{}, err
	}
	// Everything COM reads or writes through a raw address is pinned on the
	// heap (see heapNew).
	var pin runtime.Pinner
	defer pin.Unpin()
	n := heapNew[*uint16](&pin)
	*n = w
	pin.Pin(w)
	id := heapNew[uint32](&pin)
	if hr := d.call(slotGetIDsOfNames, ptr(&iidNull), ptr(n), 1, 0, ptr(id)); failed(hr) {
		return variant{}, fmt.Errorf("no member %s (HRESULT 0x%x)", name, hr)
	}
	// Invoke wants the arguments in reverse order.
	rev := make([]variant, len(args))
	for i, a := range args {
		rev[len(args)-1-i] = a
	}
	dp := heapNew[dispParams](&pin)
	dp.cArgs = uint32(len(rev)) //nolint:gosec // at most 5 arguments
	if len(rev) > 0 {
		pin.Pin(&rev[0])
		dp.args = &rev[0]
	}
	res := heapNew[variant](&pin)
	if hr := d.call(slotInvoke, uintptr(*id), ptr(&iidNull), 0, flags, ptr(dp), ptr(res), 0, 0); failed(hr) {
		return variant{}, fmt.Errorf("%s failed (HRESULT 0x%x)", name, hr)
	}
	return *res, nil
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
	if app == nil {
		return fmt.Errorf("%s: %w", hint, errExplorerUnavailable)
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
	var pin runtime.Pinner
	defer pin.Unpin()
	// All out-values live in one pinned heap block (see heapNew).
	out := heapNew[struct {
		sw, desktop, sp, sb, sv, bg *iface
		hwnd                        int32
		vLoc, vRoot                 variant
	}](&pin)
	out.vLoc = variant{vt: vtI4} // CSIDL_DESKTOP = 0
	out.vRoot = variant{vt: vtEmpty}

	hr, _, _ := procCoCreate.Call(ptr(&clsidShellWindows), 0, clsctxLocalServer, ptr(&iidShellWindows), ptr(&out.sw))
	if failed(hr) {
		return nil, fmt.Errorf("%w (HRESULT 0x%x)", errExplorerUnavailable, hr)
	}
	if out.sw == nil {
		return nil, fmt.Errorf("%w (no shell windows object)", errExplorerUnavailable)
	}
	defer out.sw.release()

	if hr := out.sw.call(slotFindWindowSW, ptr(&out.vLoc), ptr(&out.vRoot), swcDesktop, ptr(&out.hwnd), swfoNeedDispatch, ptr(&out.desktop)); failed(hr) || hr != 0 {
		return nil, fmt.Errorf("%w (no desktop shell window, HRESULT 0x%x)", errExplorerUnavailable, hr)
	}
	if out.desktop == nil {
		return nil, fmt.Errorf("%w (no desktop shell window)", errExplorerUnavailable)
	}
	defer out.desktop.release()

	if hr := out.desktop.call(slotQueryInterface, ptr(&iidServiceProv), ptr(&out.sp)); failed(hr) || out.sp == nil {
		return nil, fmt.Errorf("%w (shell window has no service provider, HRESULT 0x%x)", errExplorerUnavailable, hr)
	}
	defer out.sp.release()
	if hr := out.sp.call(slotQueryService, ptr(&sidTopLevelBrowse), ptr(&iidShellBrowser), ptr(&out.sb)); failed(hr) || out.sb == nil {
		return nil, fmt.Errorf("%w (no shell browser, HRESULT 0x%x)", errExplorerUnavailable, hr)
	}
	defer out.sb.release()
	if hr := out.sb.call(slotActiveView, ptr(&out.sv)); failed(hr) || out.sv == nil {
		return nil, fmt.Errorf("%w (no shell view, HRESULT 0x%x)", errExplorerUnavailable, hr)
	}
	defer out.sv.release()
	if hr := out.sv.call(slotGetItemObject, svgioBackground, ptr(&iidDispatch), ptr(&out.bg)); failed(hr) || out.bg == nil {
		return nil, fmt.Errorf("%w (no shell folder view, HRESULT 0x%x)", errExplorerUnavailable, hr)
	}
	defer out.bg.release()

	res, err := invoke(out.bg, "Application", dispatchPropGet)
	if err != nil {
		return nil, err
	}
	if res.vt != vtDispatch || res.val[0] == 0 {
		return nil, fmt.Errorf("%w (shell folder view returned no Application)", errExplorerUnavailable)
	}
	return *(**iface)(unsafe.Pointer(&res.val[0])), nil //nolint:gosec // reads the IDispatch pointer out of the VARIANT union
}
