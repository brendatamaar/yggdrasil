//go:build !windows

package platform

import (
	"testing"
	"time"
)

func waitTerminationExit(t *testing.T, p Process, permitExit func()) {
	t.Helper()
	permitExit()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !p.(*process).exited.Load() {
		select {
		case <-timer.C:
			t.Fatal("helper did not exit")
		case <-tick.C:
		}
	}
}
