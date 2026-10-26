package platform

import "runtime"

// Machine reports the room on this computer for the maxAgents hint (D7).
type Machine struct{}

// CPUs is the number of logical CPUs.
func (Machine) CPUs() int { return runtime.NumCPU() }

// FreeMemMB is the free physical memory in MiB, or 0 when it can't be read
// (the hint then stays quiet).
func (Machine) FreeMemMB() int { return freeMemMB() }
