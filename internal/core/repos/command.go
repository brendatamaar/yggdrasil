package repos

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Command is an effect Decide asks the shell for. It is plain data; the core
// never performs it. It is a closed set. The row commands run inside the one
// Store.Commit that persists the decision; ExpectVersion is the project's
// version before the decision, so a racing decision gets db.ErrConflict.
type Command interface{ isCommand() }

// InsertProject stores a new Active project.
type InsertProject struct {
	Project Project
}

// InsertRepo stores a new Active repo.
type InsertRepo struct {
	ProjectID     core.ProjectID
	Repo          Repo
	ExpectVersion int64
}

// RestoreProject makes an Untracked project Active again, with a new name.
type RestoreProject struct {
	ProjectID     core.ProjectID
	Name          string
	Since         time.Time
	ExpectVersion int64
}

// RestoreRepo makes an Untracked repo Active again with Trusted = false.
type RestoreRepo struct {
	ProjectID     core.ProjectID
	RepoID        core.RepoID
	Since         time.Time
	ExpectVersion int64
}

// MarkRepoUntracked hides a repo.
type MarkRepoUntracked struct {
	ProjectID     core.ProjectID
	RepoID        core.RepoID
	At            time.Time
	ExpectVersion int64
}

// MarkProjectUntracked hides a project.
type MarkProjectUntracked struct {
	ProjectID     core.ProjectID
	At            time.Time
	ExpectVersion int64
}

// UpdateProject renames a project.
type UpdateProject struct {
	ProjectID     core.ProjectID
	Name          string
	ExpectVersion int64
}

// WriteAudit is committed with the rows. Actions: project.track,
// project.restore, project.untrack, project.rename, repo.track, repo.restore,
// repo.untrack.
type WriteAudit struct {
	Actor   Actor
	Action  string
	Subject string // "project:<id>" or "repo:<id>"
	Detail  map[string]string
}

// PublishReposChanged tells subscribers that the repo list changed (after
// commit).
type PublishReposChanged struct{}

// RefreshStatus asks repo-status to read these repos (after commit).
type RefreshStatus struct{ RepoIDs []core.RepoID }

func (InsertProject) isCommand()        {}
func (InsertRepo) isCommand()           {}
func (RestoreProject) isCommand()       {}
func (RestoreRepo) isCommand()          {}
func (MarkRepoUntracked) isCommand()    {}
func (MarkProjectUntracked) isCommand() {}
func (UpdateProject) isCommand()        {}
func (WriteAudit) isCommand()           {}
func (PublishReposChanged) isCommand()  {}
func (RefreshStatus) isCommand()        {}
