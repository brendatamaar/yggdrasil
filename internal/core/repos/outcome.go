package repos

import "github.com/brendatamaar/yggdrasil/internal/core"

// Outcome is the domain result of a tracking decision, returned to the
// caller next to the new workspace. It is a closed set.
type Outcome interface{ isOutcome() }

// Tracked: the project exists and these repos were added or restored.
type Tracked struct {
	ProjectID core.ProjectID
	Added     []core.RepoID
	Restored  []core.RepoID
}

// AlreadyTracked: nothing needed to change.
type AlreadyTracked struct{ ProjectID core.ProjectID }

// NameTaken: another Active project has this name.
type NameTaken struct{ Name string }

// Overlaps: the path is inside or contains another Active project's path.
type Overlaps struct{ Other core.ProjectID }

// RepoBusy: a build or task is running in the repo.
type RepoBusy struct {
	RepoID core.RepoID
	Reason BusyReason
}

// NotFound: the project or repo is unknown or already Untracked.
type NotFound struct{}

// NothingPicked: the request selected no repo.
type NothingPicked struct{}

// UntrackDone: these repos are now Untracked (none when it already was). The
// design calls this outcome Untracked; that name is the Life variant here.
type UntrackDone struct {
	Project core.ProjectID
	Repos   []core.RepoID
}

// InvalidInput: the input broke a rule the shell's parsers should have
// caught (a bad name or relative path, an ID already in use). The core
// refuses it instead of trusting the caller.
type InvalidInput struct{ Reason string }

// Renamed: the project has its new name.
type Renamed struct{ ProjectID core.ProjectID }

func (Tracked) isOutcome()        {}
func (AlreadyTracked) isOutcome() {}
func (NameTaken) isOutcome()      {}
func (Overlaps) isOutcome()       {}
func (RepoBusy) isOutcome()       {}
func (NotFound) isOutcome()       {}
func (NothingPicked) isOutcome()  {}
func (UntrackDone) isOutcome()    {}
func (Renamed) isOutcome()        {}
func (InvalidInput) isOutcome()   {}
