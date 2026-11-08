//go:build linux

package platform

import (
	"bufio"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

// tcpListen is the "st" column value of a socket in LISTEN state.
const tcpListen = "0A"

// ListeningPorts returns the sorted TCP ports that pid and its descendants
// listen on, IPv4 and IPv6 (DV-02). It reads /proc/net/tcp and tcp6 for
// listening sockets and matches their inodes against the sockets that each
// process of the tree holds open. It fails if pid is not running.
func ListeningPorts(pid int) ([]int, error) {
	tree, err := processTree(pid)
	if err != nil {
		return nil, err
	}
	owned := map[uint64]bool{}
	for _, p := range tree {
		for _, ino := range socketInodes(p) {
			owned[ino] = true
		}
	}
	var ports []int
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(file) //nolint:gosec // fixed /proc paths
		if err != nil {
			continue // IPv6 may be disabled
		}
		for ino, port := range listeningSockets(f) {
			if owned[ino] {
				ports = append(ports, port)
			}
		}
		_ = f.Close()
	}
	slices.Sort(ports)
	return slices.Compact(ports), nil
}

// socketInodes lists the socket inodes held open by pid. Errors (the process
// ended, or its fd folder is not readable) give an empty list.
func socketInodes(pid int) []uint64 {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []uint64
	for _, e := range entries {
		link, err := os.Readlink(dir + "/" + e.Name())
		if err != nil {
			continue
		}
		if s, ok := strings.CutPrefix(link, "socket:["); ok {
			if ino, err := strconv.ParseUint(strings.TrimSuffix(s, "]"), 10, 64); err == nil {
				out = append(out, ino)
			}
		}
	}
	return out
}

// listeningSockets parses a /proc/net/tcp or tcp6 table and returns the inode
// and local port of every socket in LISTEN state. Malformed rows are skipped.
func listeningSockets(r io.Reader) map[uint64]int {
	out := map[uint64]int{}
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		// sl local_address rem_address st tx:rx tr:tm->when retrnsmt uid timeout inode ...
		f := strings.Fields(sc.Text())
		if len(f) < 10 || f[3] != tcpListen {
			continue
		}
		_, portHex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		ino, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil || ino == 0 {
			continue
		}
		out[ino] = int(port)
	}
	return out
}
