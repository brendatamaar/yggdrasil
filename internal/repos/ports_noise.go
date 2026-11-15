package repos

// Phase 6 declarations of the repos package (docs/design/phase-6-noise.md and
// phase-6-standup.md): bounded noise facts and history facts, read from the
// existing status reader and git wrapper. StatusService and GitHistoryReader
// implement these ports without writes or automatic refreshes.

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

// NoiseSource returns the noise facts of a tracked, ready repo from its last
// bounded status read: the untracked count, the sampled relative paths and
// whether the reader truncated. It never scans the filesystem and never claims
// a truncated sample is complete. An unreadable repo is ErrNoiseUnavailable.
type NoiseSource interface {
	NoiseFacts(ctx context.Context, repo core.RepoID) (corerepos.NoiseFacts, error)
}

// ErrNoiseUnavailable is returned when the repo could not be read; the caller
// labels noise as unavailable and keeps the ordinary status view.
var ErrNoiseUnavailable = errNoise("noise facts are unavailable: the repo could not be read")

type errNoise string

func (e errNoise) Error() string { return string(e) }

// HistoryCommit is one commit for the standup summary.
type HistoryCommit struct {
	OID     string
	Subject string
	Author  string
	At      time.Time
}

// HistoryReader returns the commits of a tracked, ready repo on its current
// branch within [from, to), newest first, at most max, through the safe git
// wrapper. Truncated means excess matches or incomplete capture, even with zero
// observed matches; results may not be globally newest after cutoff. A missing or unreadable repo is an
// error the standup labels as unavailable.
type HistoryReader interface {
	Commits(ctx context.Context, repo core.RepoID, from, to time.Time, max int) (commits []HistoryCommit, truncated bool, err error)
}
