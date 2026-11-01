//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	rstrtmgr                = windows.NewLazySystemDLL("rstrtmgr.dll")
	procRmStartSession      = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList           = rstrtmgr.NewProc("RmGetList")
	procRmEndSession        = rstrtmgr.NewProc("RmEndSession")
)

const (
	errMoreData     = 234
	rmSessionKeyLen = 32 // CCH_RM_SESSION_KEY
	maxRmInfo       = 64
)

// rmProcessInfo is RM_PROCESS_INFO.
type rmProcessInfo struct {
	ProcessID        uint32
	StartTime        windows.Filetime
	AppName          [256]uint16
	ServiceShortName [64]uint16
	AppType          uint32
	AppStatus        uint32
	TSSessionID      uint32
	Restartable      int32
}

// LockHolders names, best effort, the programs that may be using files in
// path (a file or a folder), through the Windows Restart Manager. It asks
// about a bounded sample: the top-level files and at most about 200 more files
// at shallow depth. It returns at most 5 display names without duplicates or
// control characters, and nil on any failure. It never closes, kills or
// changes anything, and always ends its session.
//
// Limits callers must respect in their wording ("may be using"): the Restart
// Manager sees open files only, not directory handles, so a terminal whose
// current folder is inside path is not reported; and files outside the sample
// are not asked about, so a holder can be missed.
func LockHolders(path string) (names []string) {
	defer func() {
		if recover() != nil {
			names = nil
		}
	}()
	files := sampleFiles(path)
	if len(files) == 0 {
		return nil
	}
	ptrs := make([]*uint16, 0, len(files))
	for _, f := range files {
		p, err := windows.UTF16PtrFromString(f)
		if err != nil {
			continue
		}
		ptrs = append(ptrs, p)
	}
	if len(ptrs) == 0 {
		return nil
	}
	var session uint32
	key := make([]uint16, rmSessionKeyLen+1)
	if r, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return nil
	}
	defer func() { _, _, _ = procRmEndSession.Call(uintptr(session)) }()
	r, _, _ := procRmRegisterResources.Call(uintptr(session), uintptr(len(ptrs)), uintptr(unsafe.Pointer(&ptrs[0])), 0, 0, 0, 0)
	if r != 0 {
		return nil
	}
	var needed, have, reasons uint32
	r, _, _ = procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&have)), 0, uintptr(unsafe.Pointer(&reasons)))
	if r != 0 && r != errMoreData {
		return nil
	}
	if needed == 0 {
		return nil
	}
	have = min(needed, maxRmInfo)
	info := make([]rmProcessInfo, have)
	needed = 0
	r, _, _ = procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&have)), uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&reasons)))
	if r != 0 && r != errMoreData {
		return nil
	}
	have = min(have, uint32(len(info)))
	raw := make([]string, 0, have)
	for _, pi := range info[:have] {
		raw = append(raw, windows.UTF16ToString(pi.AppName[:]))
	}
	return cleanHolderNames(raw)
}
