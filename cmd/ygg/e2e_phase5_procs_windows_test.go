//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Process helpers of TestPhase5EndToEnd/no_leftovers (Windows). Killing ygg
// closes its job object, which ends every dev server and everything they
// started (DV-05, PL-03): nothing is left to stop afterwards.
const (
	yggExe                = "ygg.exe"
	hasProcessList        = true
	hostKillLeavesServers = false
)

// descendants returns the PIDs of every process below root (Toolhelp snapshot).
func descendants(root int) []int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	parent := map[int]int{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		parent[int(e.ProcessID)] = int(e.ParentProcessID)
	}
	var out []int
	for changed := true; changed; {
		changed = false
		for pid, pp := range parent {
			if (pp != root && !hasPID(out, pp)) || hasPID(out, pid) {
				continue
			}
			out = append(out, pid)
			changed = true
		}
	}
	return out
}

func hasPID(l []int, v int) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// pidAlive reports whether the process still runs.
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false // gone (or never ours)
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// killPid ends one process at once (cleanup only).
func killPid(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.TerminateProcess(h, 1)
}
