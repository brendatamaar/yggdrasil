package repos

import (
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// View is what the UI shows for one repo. It is a closed set: Unknown, Fresh
// or Problem. A repo being read keeps its current View; "refreshing" is
// Refresher.InFlight.
type View interface{ isView() }

// Unknown: tracked but never read.
type Unknown struct{}

// Fresh: the last read succeeded.
type Fresh struct {
	Snap Snapshot
	At   time.Time
}

// Problem: the last read failed. Last is the previous good snapshot, kept so
// the row doesn't go blank.
type Problem struct {
	Kind ProblemKind
	At   time.Time
	Last *Snapshot
}

func (Unknown) isView() {}
func (Fresh) isView()   {}
func (Problem) isView() {}

// Snapshot is the git state of one repo (WS-02).
type Snapshot struct {
	Branch      string // "" when detached
	Detached    bool
	Upstream    string
	HasUpstream bool
	Ahead       int
	Behind      int
	Staged      int
	Unstaged    int
	Untracked   int
	Conflicted  int
	LastCommit  *CommitRef // nil before the first commit
}

// CommitRef is the last commit of a repo.
type CommitRef struct {
	OID     string
	Subject string
	Author  string
	At      time.Time
}

// ProblemKind says why a read failed.
type ProblemKind string

const (
	ProblemMissing   ProblemKind = "missing"
	ProblemNotARepo  ProblemKind = "not_a_repo"
	ProblemTimedOut  ProblemKind = "timed_out"
	ProblemGitFailed ProblemKind = "git_failed"
)

// Validate reports whether k is a known problem kind.
func (k ProblemKind) Validate() error {
	switch k {
	case ProblemMissing, ProblemNotARepo, ProblemTimedOut, ProblemGitFailed:
		return nil
	}
	return fmt.Errorf("unknown problem kind %q", string(k))
}

// Flag is a health flag derived from a View.
type Flag string

const (
	FlagMissing    Flag = "missing"
	FlagNotARepo   Flag = "not_a_repo"
	FlagTimedOut   Flag = "timed_out"
	FlagGitFailed  Flag = "git_failed"
	FlagDetached   Flag = "detached"
	FlagNoUpstream Flag = "no_upstream"
	FlagConflicts  Flag = "conflicts"
	FlagDiverged   Flag = "diverged"
)

// Validate reports whether f is a known flag.
func (f Flag) Validate() error {
	switch f {
	case FlagMissing, FlagNotARepo, FlagTimedOut, FlagGitFailed,
		FlagDetached, FlagNoUpstream, FlagConflicts, FlagDiverged:
		return nil
	}
	return fmt.Errorf("unknown flag %q", string(f))
}

// Flags derives the health flags of a view. "Problems" (WS-03) means any flag.
func Flags(v View) []Flag {
	switch x := v.(type) {
	case Problem:
		switch x.Kind {
		case ProblemMissing:
			return []Flag{FlagMissing}
		case ProblemNotARepo:
			return []Flag{FlagNotARepo}
		case ProblemTimedOut:
			return []Flag{FlagTimedOut}
		case ProblemGitFailed:
			return []Flag{FlagGitFailed}
		}
	case Fresh:
		var fl []Flag
		if x.Snap.Detached {
			fl = append(fl, FlagDetached)
		}
		if !x.Snap.HasUpstream {
			fl = append(fl, FlagNoUpstream)
		}
		if x.Snap.Conflicted > 0 {
			fl = append(fl, FlagConflicts)
		}
		if x.Snap.Ahead > 0 && x.Snap.Behind > 0 {
			fl = append(fl, FlagDiverged)
		}
		return fl
	}
	return nil
}

// SnapshotOf returns the numbers a view shows: the fresh snapshot, or the last
// good one of a Problem view. ok is false when there are none.
func SnapshotOf(v View) (Snapshot, bool) {
	switch x := v.(type) {
	case Fresh:
		return x.Snap, true
	case Problem:
		if x.Last != nil {
			return *x.Last, true
		}
	}
	return Snapshot{}, false
}

// ViewAt returns when the view was read; ok is false for Unknown.
func ViewAt(v View) (time.Time, bool) {
	switch x := v.(type) {
	case Fresh:
		return x.At, true
	case Problem:
		return x.At, true
	}
	return time.Time{}, false
}

// Filter is a WS-03 repo filter. It is a closed set.
type Filter string

const (
	FilterAll            Filter = "all"
	FilterUncommitted    Filter = "uncommitted"
	FilterBehind         Filter = "behind"
	FilterAhead          Filter = "ahead"
	FilterFailedPipeline Filter = "failed_pipeline"
	FilterProblems       Filter = "problems"
)

// Validate reports whether f is a known filter.
func (f Filter) Validate() error {
	switch f {
	case FilterAll, FilterUncommitted, FilterBehind, FilterAhead, FilterFailedPipeline, FilterProblems:
		return nil
	}
	return fmt.Errorf("unknown filter %q", string(f))
}

// Matches reports whether a repo with this view (and failed-pipeline fact,
// from git-hosts) belongs under the filter.
func (f Filter) Matches(v View, failedPipeline bool) bool {
	snap, has := SnapshotOf(v)
	switch f {
	case FilterAll:
		return true
	case FilterUncommitted:
		return has && snap.Staged+snap.Unstaged+snap.Untracked+snap.Conflicted > 0
	case FilterBehind:
		return has && snap.Behind > 0
	case FilterAhead:
		return has && snap.Ahead > 0
	case FilterFailedPipeline:
		return failedPipeline
	case FilterProblems:
		return len(Flags(v)) > 0
	}
	return false
}

// Counts has one number per WS-03 filter (WS-04).
type Counts struct {
	All            int
	Uncommitted    int
	Behind         int
	Ahead          int
	FailedPipeline int
	Problems       int
}

// CountEntries counts the entries per filter. failedPipelines comes from
// git-hosts; the status core does not know about hosts. Per-project counts are
// CountEntries over the entries of one project.
func CountEntries(entries map[core.RepoID]Entry, failedPipelines map[core.RepoID]struct{}) Counts {
	var c Counts
	for id, e := range entries {
		_, failed := failedPipelines[id]
		if FilterAll.Matches(e.View, failed) {
			c.All++
		}
		if FilterUncommitted.Matches(e.View, failed) {
			c.Uncommitted++
		}
		if FilterBehind.Matches(e.View, failed) {
			c.Behind++
		}
		if FilterAhead.Matches(e.View, failed) {
			c.Ahead++
		}
		if FilterFailedPipeline.Matches(e.View, failed) {
			c.FailedPipeline++
		}
		if FilterProblems.Matches(e.View, failed) {
			c.Problems++
		}
	}
	return c
}
