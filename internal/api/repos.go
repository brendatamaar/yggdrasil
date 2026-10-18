package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

// This file is owned by the repo-tracking task (WS-01). It holds the
// tracking service interface and routesRepos. The DTOs are in types_repos.go.
// The file is excluded from tygo (tygo.yaml): it holds Go-only wiring.

// TrackingService is implemented by internal/repos (docs/design/repo-tracking.md).
// Folder paths and names arrive as the user typed them: the service parses
// them with its boundary parsers (the filesystem checks live there) and
// returns an apperr.Invalid error that says which rule broke. Every method
// returns errors classified by internal/apperr.
type TrackingService interface {
	// BrowseDirs lists the subfolders of path for the folder browser; an
	// empty path lists the roots (drives on Windows).
	BrowseDirs(ctx context.Context, path string) (DirListingResponse, error)
	// Preview scans path and sets what it found against the workspace.
	Preview(ctx context.Context, path string) (corerepos.Preview, error)
	// Projects returns the tracked projects with their tracked repos.
	Projects(ctx context.Context) ([]corerepos.Project, error)
	// Track tracks path as a project with the picked repos (or the same path
	// again, which restores it). The service fills the new IDs.
	Track(ctx context.Context, path, name string, picks []corerepos.Pick, actor corerepos.Actor) (corerepos.Outcome, error)
	// Rescan compares a project's repos with its folder.
	Rescan(ctx context.Context, project core.ProjectID) (corerepos.RescanReport, error)
	// TrackRepos adds or restores repos of a tracked project.
	TrackRepos(ctx context.Context, project core.ProjectID, picks []corerepos.Pick, actor corerepos.Actor) (corerepos.Outcome, error)
	// UntrackRepo hides a repo. Nothing on disk is touched.
	UntrackRepo(ctx context.Context, repo core.RepoID, actor corerepos.Actor) (corerepos.Outcome, error)
	// UntrackProject hides a project and its repos.
	UntrackProject(ctx context.Context, project core.ProjectID, actor corerepos.Actor) (corerepos.Outcome, error)
	// RenameProject changes a project's name.
	RenameProject(ctx context.Context, project core.ProjectID, name string, actor corerepos.Actor) (corerepos.Outcome, error)
}

// routesRepos registers the tracking endpoints, and the status endpoints
// through routesReposStatus (repos_status.go). Each part registers nothing
// when its service is not wired.
func (s *Server) routesRepos(mux *http.ServeMux) {
	s.routesReposStatus(mux)
	if s.tracking == nil {
		return
	}
	_ = mux // the repo-tracking task adds its routes here
}
