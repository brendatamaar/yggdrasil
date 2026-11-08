package worktreeenv

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// PlatformProber finds busy ports from the operating system's listener table
// (platform.BusyPorts): one read, no bind attempts, and a port bound to any
// address counts as busy.
type PlatformProber struct{}

// Busy implements PortProber.
func (PlatformProber) Busy(ctx context.Context, lo, hi int) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return platform.BusyPorts(lo, hi)
}
