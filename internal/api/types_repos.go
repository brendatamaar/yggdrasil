package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of the repos area (docs/design/repo-tracking.md and
// docs/design/repo-status.md). They are plain; the repos task may refine them
// in this file. The core types have no JSON tags, so every conversion happens
// in the handlers.

// DirEntryDTO is one folder in the folder browser (GET /api/fs/dirs).
type DirEntryDTO struct {
	Name string `json:"name"`
	// Path is the full path to send back to browse deeper or to preview.
	Path string `json:"path"`
	// IsRepo: the folder has a .git folder or file.
	IsRepo bool `json:"isRepo"`
}

// DirListingResponse is the body of GET /api/fs/dirs?path=. An empty path
// lists the roots (drives on Windows).
type DirListingResponse struct {
	Path string `json:"path"`
	// Parent is "" at the roots.
	Parent  string        `json:"parent"`
	Entries []DirEntryDTO `json:"entries"`
}

// PreviewRequest is the body of POST /api/projects/preview.
type PreviewRequest struct {
	Path string `json:"path"`
}

// CandidateDTO is a git repo found under a folder.
type CandidateDTO struct {
	// RelPath is under the project folder with forward slashes, "." for the
	// folder itself.
	RelPath string `json:"relPath"`
	Name    string `json:"name"`
	// KnownRepoID is the repo's row in the project, when there is one.
	KnownRepoID *core.RepoID `json:"knownRepoId,omitempty"`
	// Active: that row is tracked.
	Active bool `json:"active"`
}

// PreviewResponse is the body of POST /api/projects/preview.
type PreviewResponse struct {
	Path       string         `json:"path"`
	Candidates []CandidateDTO `json:"candidates"`
	// Truncated: the scan hit its limits ("showing the first N").
	Truncated bool `json:"truncated"`
	// Skipped counts subfolders that could not be read.
	Skipped int `json:"skipped"`
	// ExistingProjectID is set when the path is already a project.
	ExistingProjectID *core.ProjectID `json:"existingProjectId,omitempty"`
}

// PickDTO is one candidate the user selected.
type PickDTO struct {
	RelPath string `json:"relPath"`
	Name    string `json:"name"`
}

// TrackProjectRequest is the body of POST /api/projects.
type TrackProjectRequest struct {
	Path  string    `json:"path"`
	Name  string    `json:"name"`
	Picks []PickDTO `json:"picks"`
}

// TrackReposRequest is the body of POST /api/projects/{id}/repos.
type TrackReposRequest struct {
	Picks []PickDTO `json:"picks"`
}

// RenameProjectRequest is the body of PATCH /api/projects/{id}.
type RenameProjectRequest struct {
	Name string `json:"name"`
}

// TrackResponse is the body of the tracking endpoints that succeed (201 for a
// new project, 200 otherwise). Conflicts are error responses (409).
type TrackResponse struct {
	ProjectID core.ProjectID `json:"projectId"`
	// Added and Restored are the repos tracked for the first time and the
	// repos that came back with their history.
	Added    []core.RepoID `json:"added"`
	Restored []core.RepoID `json:"restored"`
	// AlreadyTracked: nothing needed to change.
	AlreadyTracked bool `json:"alreadyTracked"`
}

// RescanResponse is the body of POST /api/projects/{id}/rescan.
type RescanResponse struct {
	New []CandidateDTO `json:"new"`
	// Missing are tracked repos whose folder is gone or no longer a repo;
	// offer "stop tracking", never untrack automatically.
	Missing []core.RepoID `json:"missing"`
}

// RepoViewKind says what the status of a repo holds.
type RepoViewKind string

const (
	// RepoViewUnknown: never read yet.
	RepoViewUnknown RepoViewKind = "unknown"
	// RepoViewFresh: the last read worked.
	RepoViewFresh RepoViewKind = "fresh"
	// RepoViewProblem: the last read failed; the numbers are the last good ones.
	RepoViewProblem RepoViewKind = "problem"
)

// CommitRefDTO is the last commit of a repo.
type CommitRefDTO struct {
	OID     string    `json:"oid"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	At      time.Time `json:"at"`
}

// RepoStatusDTO is the status of one repo (WS-02). It is also the data of a
// "repo:<id>" message.
type RepoStatusDTO struct {
	ID        core.RepoID    `json:"id"`
	ProjectID core.ProjectID `json:"projectId"`
	Name      string         `json:"name"`
	// RelPath is under the project folder, "." for the folder itself.
	RelPath string `json:"relPath"`
	Path    string `json:"path"`
	Trusted bool   `json:"trusted"`
	// Refreshing: a read is in flight; the numbers are the last known ones.
	Refreshing bool         `json:"refreshing"`
	View       RepoViewKind `json:"view"`
	// Problem is one of missing, not_a_repo, timed_out, git_failed when View
	// is "problem", else empty. ProblemMessage says what to do next.
	Problem        string `json:"problem,omitempty"`
	ProblemMessage string `json:"problemMessage,omitempty"`
	// Flags are the health flags: missing, not_a_repo, timed_out, git_failed,
	// detached, no_upstream, conflicts, diverged. "Problems" means any flag.
	Flags []string `json:"flags"`
	// CheckedAt is when the numbers were read.
	CheckedAt   *time.Time    `json:"checkedAt,omitempty"`
	Branch      string        `json:"branch"`
	Detached    bool          `json:"detached"`
	Upstream    string        `json:"upstream"`
	HasUpstream bool          `json:"hasUpstream"`
	Ahead       int           `json:"ahead"`
	Behind      int           `json:"behind"`
	Staged      int           `json:"staged"`
	Unstaged    int           `json:"unstaged"`
	Untracked   int           `json:"untracked"`
	Conflicted  int           `json:"conflicted"`
	LastCommit  *CommitRefDTO `json:"lastCommit,omitempty"`
	// FailedPipeline: the current branch's pipeline failed (from git hosts).
	FailedPipeline bool `json:"failedPipeline"`
}

// RepoCountsDTO has one number per filter (WS-03, WS-04).
type RepoCountsDTO struct {
	All            int `json:"all"`
	Uncommitted    int `json:"uncommitted"`
	Behind         int `json:"behind"`
	Ahead          int `json:"ahead"`
	FailedPipeline int `json:"failedPipeline"`
	Problems       int `json:"problems"`
}

// ProjectDTO is a tracked project with its tracked repos and counts.
type ProjectDTO struct {
	ID     core.ProjectID  `json:"id"`
	Name   string          `json:"name"`
	Path   string          `json:"path"`
	Repos  []RepoStatusDTO `json:"repos"`
	Counts RepoCountsDTO   `json:"counts"`
}

// ReposResponse is the body of GET /api/repos?project=&filter=. Filter is one
// of all, uncommitted, behind, ahead, failed_pipeline, problems; the counts
// are for the unfiltered set.
type ReposResponse struct {
	Projects []ProjectDTO  `json:"projects"`
	Counts   RepoCountsDTO `json:"counts"`
}

// RefreshReposRequest is the body of POST /api/repos/refresh. Empty IDs means
// all repos. Reason is one of user, focus, git_action, build, tracking, tick;
// empty means user.
type RefreshReposRequest struct {
	IDs    []core.RepoID `json:"ids"`
	Reason string        `json:"reason,omitempty"`
}

// ReposChangedMessage is the data of a message on the "repos" topic: the list
// or the counts changed. The client refetches the list.
type ReposChangedMessage struct {
	Counts RepoCountsDTO `json:"counts"`
}
