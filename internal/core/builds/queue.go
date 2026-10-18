package builds

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// QueuedRef is what admission needs to know about a queued build.
type QueuedRef struct {
	ID       core.BuildID
	QueuedAt time.Time
}

// Next picks the build to admit: none when running >= max, else the oldest
// (QueuedAt, ID) of queued. At v1, max is 1 (BL-03).
func Next(queued []QueuedRef, running, max int) (core.BuildID, bool) {
	if running >= max || len(queued) == 0 {
		return "", false
	}
	best := queued[0]
	for _, q := range queued[1:] {
		if q.QueuedAt.Before(best.QueuedAt) || (q.QueuedAt.Equal(best.QueuedAt) && q.ID < best.ID) {
			best = q
		}
	}
	return best.ID, true
}
