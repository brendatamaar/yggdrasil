package platformtest_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

func TestFakeMeetsOrphanContract(t *testing.T) {
	platformtest.OrphanContract(t, func(*testing.T) platformtest.OrphanWorld {
		f := platformtest.NewFakeOrphans()
		return platformtest.OrphanWorld{
			Orphans: f.Orphans(),
			Spawn:   func(*testing.T) int { return f.Spawn() },
			Alive:   f.Alive,
		}
	})
}
