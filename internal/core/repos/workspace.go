package repos

import (
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Actor is who asked for a change: "user", "agent:<name>" or "system".
type Actor string

// Workspace is the tracking aggregate: every project and repo row, tracked or
// not. It is small, so one transaction loads all of it, decides and writes.
type Workspace struct {
	Projects map[core.ProjectID]Project
	// Busy is loaded by the shell: repos with a running build or task.
	Busy map[core.RepoID]BusyReason
}

// Project is a tracked (or untracked) folder with its repos.
type Project struct {
	ID   core.ProjectID
	Name string
	Path CleanPath
	Life Life
	// Repos holds the project's repos, tracked or not.
	Repos   map[core.RepoID]Repo
	Version int64
}

// Repo is a git repository inside a project folder.
type Repo struct {
	ID   core.RepoID
	Name string // unique among the project's repos, tracked or not
	// RelPath is under Project.Path; "." for the folder itself.
	RelPath RelPath
	Life    Life
	Trusted bool // SF-09
}

// Life says whether a project or repo is listed. It is a closed set: Active
// or Untracked. Untracked rows stay in the database with their build history
// and are invisible to every list and to agents (SF-02).
type Life interface{ isLife() }

// Active is listed, visible to agents and refreshed by repo-status.
type Active struct{ Since time.Time }

// Untracked is hidden everywhere. Tracking the same path again restores it.
type Untracked struct{ At time.Time }

func (Active) isLife()    {}
func (Untracked) isLife() {}

// BusyReason says why a repo cannot be untracked right now.
type BusyReason string

const (
	BusyBuildRunning BusyReason = "build_running"
	BusyTaskRunning  BusyReason = "task_running"
)

// Validate reports whether r is a known reason.
func (r BusyReason) Validate() error {
	switch r {
	case BusyBuildRunning, BusyTaskRunning:
		return nil
	}
	return fmt.Errorf("unknown busy reason %q", string(r))
}

// Pick is one selected candidate. The shell fills NewRepoID for every pick;
// the core uses it only when no row exists for that RelPath.
type Pick struct {
	RelPath   RelPath
	Name      string
	NewRepoID core.RepoID
}

// Preview is the read model of "track this folder": what a scan found, set
// against the workspace.
type Preview struct {
	Path       CleanPath
	Candidates []Candidate
	// Truncated: the scan hit its limits.
	Truncated bool
	// Skipped counts subfolders the scan could not read.
	Skipped int
	// Existing is set when Path is already a project (active or untracked).
	Existing *core.ProjectID
}

// Candidate is a git repo found under a folder.
type Candidate struct {
	RelPath RelPath
	Name    string
	// Known is the repo's row in that project, if any.
	Known *core.RepoID
	// Active: that row is Active.
	Active bool
}

// ScanResult is what the read-only scan of a folder found. It is data from an
// effect, handed to BuildPreview and Rescan.
type ScanResult struct {
	Repos     []RelPath
	Truncated bool
	Skipped   int
}

// RescanReport compares a project's rows with its folder.
type RescanReport struct {
	// New: found on disk, no row.
	New []Candidate
	// Missing: Active rows whose folder is gone or no longer a repo.
	Missing []core.RepoID
}
