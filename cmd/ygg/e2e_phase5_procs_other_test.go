//go:build !linux && !windows

package main

// Process helpers of TestPhase5EndToEnd/no_leftovers on other systems: there
// is no process listing here, so the test checks the dev server ports only.
const (
	yggExe                = "ygg"
	hasProcessList        = false
	hostKillLeavesServers = true
)

func descendants(int) []int { return nil }
func pidAlive(int) bool     { return false }
func killPid(int)           {}
