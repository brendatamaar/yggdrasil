//go:build linux

package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procStat is the part of /proc/<pid>/stat that we use.
type procStat struct {
	state byte   // R, S, D, Z, ...
	ppid  int    // parent pid
	start string // starttime in clock ticks since boot: identifies the instance
}

// parseProcStat parses the content of /proc/<pid>/stat. The command name is in
// parentheses and may contain spaces and parentheses, so the fields are
// counted after the LAST ')'.
func parseProcStat(data string) (procStat, error) {
	end := strings.LastIndexByte(data, ')')
	if end < 0 {
		return procStat{}, errors.New("malformed /proc stat")
	}
	f := strings.Fields(data[end+1:])
	// f[0] is field 3 (state); field 4 is ppid; field 22 is starttime.
	if len(f) < 20 || len(f[0]) != 1 {
		return procStat{}, errors.New("malformed /proc stat")
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return procStat{}, fmt.Errorf("malformed /proc stat: %w", err)
	}
	return procStat{state: f[0][0], ppid: ppid, start: f[19]}, nil
}

func readProcStat(pid int) (procStat, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	return parseProcStat(string(data))
}

// processStart returns a token that identifies the running instance of pid
// (its start time). A zombie counts as ended: it runs nothing.
func processStart(pid int) (string, error) {
	st, err := readProcStat(pid)
	if err != nil {
		return "", err
	}
	if st.state == 'Z' || st.state == 'X' {
		return "", errors.New("process has ended")
	}
	return st.start, nil
}

// processTree returns pid and all its descendants, by scanning /proc.
func processTree(pid int) ([]int, error) {
	if _, err := processStart(pid); err != nil {
		return nil, fmt.Errorf("process %d is not running: %w", pid, err)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	children := map[int][]int{}
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if st, err := readProcStat(p); err == nil {
			children[st.ppid] = append(children[st.ppid], p)
		}
	}
	tree := []int{pid}
	for i := 0; i < len(tree); i++ {
		tree = append(tree, children[tree[i]]...)
	}
	return tree, nil
}
