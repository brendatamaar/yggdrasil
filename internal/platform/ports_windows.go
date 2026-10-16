//go:build windows

package platform

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const (
	afInet6                = 23
	tcpTableOwnerPIDListen = 3 // TCP_TABLE_OWNER_PID_LISTENER

	// Row layouts of MIB_TCPROW_OWNER_PID and MIB_TCP6ROW_OWNER_PID: the row
	// size, the offset of the local port and the offset of the owning pid.
	row4Size, row4Port, row4PID = 24, 8, 20
	row6Size, row6Port, row6PID = 56, 20, 52
)

// ListeningPorts returns the sorted TCP ports that pid and its descendants
// listen on, IPv4 and IPv6 (DV-02). It walks the process tree with Toolhelp
// and reads the kernel's TCP table; no netstat, no shell. It fails if pid is
// not running.
func ListeningPorts(pid int) ([]int, error) {
	tree, err := processTree(pid)
	if err != nil {
		return nil, err
	}
	var ports []int
	for _, t := range []struct {
		af            uint32
		size, port, p int
	}{
		{windows.AF_INET, row4Size, row4Port, row4PID},
		{afInet6, row6Size, row6Port, row6PID},
	} {
		table, err := tcpListenTable(t.af)
		if err != nil {
			return nil, err
		}
		ports = append(ports, tablePorts(table, t.size, t.port, t.p, tree)...)
	}
	slices.Sort(ports)
	return slices.Compact(ports), nil
}

// processTree returns pid and all its descendants.
func processTree(pid int) (map[uint32]bool, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	children := map[uint32][]uint32{}
	found := false
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))} //nolint:gosec // fixed struct size
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		found = found || int(entry.ProcessID) == pid
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("walk processes: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("platform: process %d is not running", pid)
	}
	tree := map[uint32]bool{}
	todo := []uint32{uint32(pid)} //nolint:gosec // pids are positive 32-bit values
	for len(todo) > 0 {
		n := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if tree[n] {
			continue // a reused parent pid can make a cycle
		}
		tree[n] = true
		todo = append(todo, children[n]...)
	}
	return tree, nil
}

// tcpListenTable reads the listener table of one address family. The table
// can grow between the size query and the read, so it retries.
func tcpListenTable(af uint32) ([]byte, error) {
	size := uint32(16 * 1024)
	for range 8 {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), //nolint:gosec // raw Win32 call
			0, uintptr(af), tcpTableOwnerPIDListen, 0)
		switch windows.Errno(r) {
		case 0:
			return buf[:size], nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
		default:
			return nil, fmt.Errorf("GetExtendedTcpTable: %w", windows.Errno(r))
		}
	}
	return nil, errors.New("GetExtendedTcpTable: table keeps growing")
}

// tablePorts returns the local ports of the rows owned by a pid in tree. A
// table is a uint32 row count followed by the rows; ports are big-endian in
// the low two bytes of their field.
func tablePorts(table []byte, rowSize, portOff, pidOff int, tree map[uint32]bool) []int {
	if len(table) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(table))
	var ports []int
	for i := range n {
		row := table[4+i*rowSize:]
		if len(row) < rowSize {
			break
		}
		if tree[binary.LittleEndian.Uint32(row[pidOff:])] {
			ports = append(ports, int(binary.BigEndian.Uint16(row[portOff:])))
		}
	}
	return ports
}
