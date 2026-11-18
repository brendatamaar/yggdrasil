package standup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// buildScanLimit is the most builds of one repo a read looks at: the newest
// ones by queue time, and the most db.BuildFilter allows. The store has no read
// by start or finish time, so the window is applied here on that bounded read.
const buildScanLimit = 1000

// BuildStore is the part of db.Store the build source reads. Both reads are
// plain database reads.
type BuildStore interface {
	// GetRepo returns a tracked repo; an untracked one is db.ErrNotFound.
	GetRepo(ctx context.Context, id core.RepoID) (core.Repo, error)
	// ListBuildStates returns builds newest first (by queue time).
	ListBuildStates(ctx context.Context, f db.BuildFilter) ([]corebuilds.State, error)
}

// StoreBuilds is the BuildSource over the build store (AU-02).
type StoreBuilds struct{ store BuildStore }

var _ BuildSource = (*StoreBuilds)(nil)

// NewBuildSource returns the build source over store.
func NewBuildSource(store BuildStore) (*StoreBuilds, error) {
	if store == nil {
		return nil, errors.New("standup: the build source needs a store")
	}
	return &StoreBuilds{store: store}, nil
}

// Builds returns the builds of repo with a start or finish time in [from, to),
// newest first (ties by build ID), at most max; the bool is true when more
// matched. An untracked repo is an error, as is a cancelled context.
func (s *StoreBuilds) Builds(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]corestandup.Build, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("standup builds: %w", err)
	}
	if err := repo.Validate(); err != nil || max < 1 || !from.Before(to) {
		return nil, false, fmt.Errorf("%w: build read arguments", errInvalidData)
	}
	r, err := s.store.GetRepo(ctx, repo)
	if err != nil {
		return nil, false, fmt.Errorf("standup builds: repo %s: %w", repo, err)
	}
	states, err := s.store.ListBuildStates(ctx, db.BuildFilter{Repo: repo, Limit: buildScanLimit})
	if err != nil {
		return nil, false, fmt.Errorf("standup builds: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("standup builds: %w", err)
	}
	all := make([]corestandup.Build, 0, len(states))
	var oldestQueued time.Time
	for _, st := range states {
		b, ok, err := buildOf(st, repo, from, to)
		if err != nil {
			return nil, false, err
		}
		if q, known := queuedAt(st); known && (oldestQueued.IsZero() || q.Before(oldestQueued)) {
			oldestQueued = q
		}
		if ok {
			b.RepoName = r.Name
			all = append(all, b)
		}
	}
	out, cut := newestBuilds(all, max)
	// The scan saw only the newest builds: when it was full and still reached
	// back to the window, older matches may exist, so say the list is cut.
	if len(states) >= buildScanLimit && !oldestQueued.IsZero() && !oldestQueued.Before(from) {
		cut = true
	}
	return out, cut, nil
}

// queuedAt is the queue time of a build state.
func queuedAt(st corebuilds.State) (time.Time, bool) {
	switch v := st.(type) {
	case corebuilds.Queued:
		return v.QueuedAt, true
	case corebuilds.Running:
		return v.QueuedAt, true
	case corebuilds.Cancelling:
		return v.QueuedAt, true
	case corebuilds.Finished:
		return v.QueuedAt, true
	}
	return time.Time{}, false
}

// buildOf parses one stored state into a standup build. ok is false when the
// build has no start or finish time in [from, to) (a queued build has neither).
// At is the later of the in-window start and finish instants. An invalid state
// (no ID, a spec without the repo, an unknown status) is an error: the store
// is not trusted to be well formed.
func buildOf(st corebuilds.State, repo core.RepoID, from, to time.Time) (corestandup.Build, bool, error) {
	var (
		id      core.BuildID
		spec    corebuilds.Spec
		started *time.Time
		done    *time.Time
	)
	switch v := st.(type) {
	case corebuilds.Queued:
		return corestandup.Build{}, false, nil
	case corebuilds.Running:
		id, spec, started = v.ID, v.Spec, &v.StartedAt
	case corebuilds.Cancelling:
		id, spec, started = v.ID, v.Spec, &v.StartedAt
	case corebuilds.Finished:
		id, spec, started, done = v.ID, v.Spec, v.StartedAt, &v.FinishedAt
	default:
		return corestandup.Build{}, false, fmt.Errorf("%w: unknown build state %T", errInvalidData, st)
	}
	status := corebuilds.Status(st)
	if err := id.Validate(); err != nil || status.Validate() != nil || !specHasRepo(spec, repo) {
		return corestandup.Build{}, false, fmt.Errorf("%w: build %q", errInvalidData, string(id))
	}
	var at time.Time
	for _, t := range []*time.Time{started, done} {
		if t != nil && !t.IsZero() && !t.Before(from) && t.Before(to) && t.After(at) {
			at = *t
		}
	}
	if at.IsZero() {
		return corestandup.Build{}, false, nil
	}
	return corestandup.Build{Repo: repo, ID: id, Status: status, At: at.UTC()}, true, nil
}

func specHasRepo(spec corebuilds.Spec, repo core.RepoID) bool {
	return slices.ContainsFunc(spec.Steps, func(s corebuilds.StepSpec) bool { return s.RepoID == repo })
}

// newestBuilds orders newest first (ties by ID) and keeps at most max; the bool
// is true when something was cut.
func newestBuilds(in []corestandup.Build, max int) ([]corestandup.Build, bool) {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b corestandup.Build) int {
		return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.ID, b.ID))
	})
	if len(out) > max {
		return out[:max], true
	}
	return out, false
}
