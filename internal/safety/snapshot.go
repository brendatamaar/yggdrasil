package safety

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// Snapshotter fingerprints a working tree for the Ask-mode integrity check
// (SF-07, invariant 9). The real implementation is (*git.Git).Snapshot; it
// lives in internal/git because only that package runs git. Two snapshots are
// Equal exactly when nothing in the tree changed: tracked content and modes,
// untracked files and their content, HEAD. Ignored files do not take part.
type Snapshotter interface {
	Snapshot(ctx context.Context, dir string) (guard.Snapshot, error)
}
