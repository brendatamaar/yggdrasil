//go:build !windows

package platform_test

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// The real orphan functions must pass the same contract as the fake.
func TestRealOrphansMeetContract(t *testing.T) {
	platformtest.OrphanContract(t, func(t *testing.T) platformtest.OrphanWorld {
		dataDir := filepath.Join(t.TempDir(), "data dir")
		return platformtest.OrphanWorld{
			Orphans: platformtest.RealOrphans(dataDir),
			Spawn: func(t *testing.T) int {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				p, err := platform.Start(ctx, platform.Spec{
					Argv: []string{"/bin/sh", "-c", "exec sleep 120"},
					Env:  []string{"PATH=/usr/bin:/bin"},
				})
				if err != nil {
					t.Fatal(err)
				}
				return p.Pid()
			},
			Alive: func(pid int) bool {
				// A killed process is reaped by Start's goroutine a moment later.
				deadline := time.Now().Add(500 * time.Millisecond)
				for {
					if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
						return false
					}
					if time.Now().After(deadline) {
						return true
					}
					time.Sleep(10 * time.Millisecond)
				}
			},
		}
	})
}
