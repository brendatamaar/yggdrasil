//go:build !windows

package platform

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// freeMemMB reads MemAvailable from /proc/meminfo; other systems report 0.
func freeMemMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "MemAvailable:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		kb, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}
