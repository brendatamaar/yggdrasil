//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Process helpers of TestPhase5EndToEnd/no_leftovers (Linux). There is no job
// object on Linux: a server of a killed ygg survives, and the next start
// reports it as orphaned so the user can stop it (docs/design/devservers.md).
const (
	yggExe                = "ygg"
	hasProcessList        = true
	hostKillLeavesServers = true
)

// descendants returns the PIDs of every process below root, read from /proc.
func descendants(root int) []int {
	parent := map[int]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// "pid (comm) S ppid ...": comm may hold spaces and parentheses.
		s := string(raw)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		parent[pid] = ppid
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

// pidAlive reports whether a process exists and is not a zombie.
func pidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && !strings.HasPrefix(strings.TrimSpace(s[i+1:]), "Z")
}

// killPid ends one process at once (cleanup only).
func killPid(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
