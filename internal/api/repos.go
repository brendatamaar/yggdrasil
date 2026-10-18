package api

import (
	"context"
	"fmt"
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
	BrowseDirs(ctx context.Context, path string) (corerepos.DirListing, error)
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
	mux.HandleFunc("GET /api/fs/dirs", s.handleBrowse)
	mux.HandleFunc("POST /api/projects/preview", s.handlePreview)
	mux.HandleFunc("POST /api/projects", s.handleTrackProject)
	mux.HandleFunc("POST /api/projects/{id}/rescan", s.handleRescan)
	mux.HandleFunc("POST /api/projects/{id}/repos", s.handleTrackRepos)
	mux.HandleFunc("POST /api/projects/{id}/untrack", s.handleUntrackProject)
	mux.HandleFunc("PATCH /api/projects/{id}", s.handleRenameProject)
	mux.HandleFunc("POST /api/repos/{id}/untrack", s.handleUntrackRepo)
}

// trackingActor is who asks: every tracking request comes from the signed-in
// person at the browser.
const trackingActor corerepos.Actor = "user"

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	out, err := s.tracking.BrowseDirs(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dirListingDTO(out))
}

func dirListingDTO(l corerepos.DirListing) DirListingResponse {
	out := DirListingResponse{Path: l.Path, Parent: l.Parent, Entries: make([]DirEntryDTO, 0, len(l.Entries))}
	for _, e := range l.Entries {
		out.Entries = append(out.Entries, DirEntryDTO{Name: e.Name, Path: e.Path, IsRepo: e.IsRepo})
	}
	return out
}

func candidateDTO(c corerepos.Candidate) CandidateDTO {
	return CandidateDTO{RelPath: string(c.RelPath), Name: c.Name, KnownRepoID: c.Known, Active: c.Active}
}

func candidateDTOs(cs []corerepos.Candidate) []CandidateDTO {
	out := make([]CandidateDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, candidateDTO(c))
	}
	return out
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var req PreviewRequest
	if !decodeBody(w, r, &req) {
		return
	}
	pv, err := s.tracking.Preview(r.Context(), req.Path)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, PreviewResponse{
		Path: string(pv.Path), Candidates: candidateDTOs(pv.Candidates), Truncated: pv.Truncated,
		Skipped: pv.Skipped, ExistingProjectID: pv.Existing,
	})
}

func picksOf(dtos []PickDTO) []corerepos.Pick {
	out := make([]corerepos.Pick, 0, len(dtos))
	for _, d := range dtos {
		// Unchecked on purpose: the service's ParsePicks is the one parser.
		out = append(out, corerepos.Pick{RelPath: corerepos.RelPath(d.RelPath), Name: d.Name})
	}
	return out
}

// projectID parses the {id} path value.
func projectID(w http.ResponseWriter, r *http.Request) (core.ProjectID, bool) {
	id := core.ProjectID(r.PathValue("id"))
	if err := id.Validate(); err != nil {
		badRequest(w, "That isn't a project ID.", "Reload the page and try again.")
		return "", false
	}
	return id, true
}

// publishRepos tells subscribers that the repo list changed. The client
// refetches the list; the counts are the status service's to fill.
func (s *Server) publishRepos() {
	s.hub.publishNext(reposTopic{}, ReposChangedMessage{})
}

// trackFailure writes the response of an outcome that is not a success and
// reports whether it did. This is the outcome-to-status mapping of the design.
func (s *Server) trackFailure(w http.ResponseWriter, out corerepos.Outcome) bool {
	switch o := out.(type) {
	case corerepos.NameTaken:
		writeError(w, http.StatusConflict, ErrConflict, fmt.Sprintf("A project called %q is already tracked.", o.Name),
			"Choose another name, or stop tracking the other project first.")
	case corerepos.Overlaps:
		writeError(w, http.StatusConflict, ErrConflict, "This folder overlaps a project that is already tracked.",
			"Track a folder that is not inside, or around, another tracked folder.")
	case corerepos.RepoBusy:
		if o.Reason == corerepos.BusyTaskRunning {
			writeError(w, http.StatusConflict, ErrConflict, "An agent task is queued or running in this repo, so it can't be untracked now.",
				"Wait for the task to finish or cancel it, then try again.")
		} else {
			writeError(w, http.StatusConflict, ErrConflict, "A build is running in this repo, so it can't be untracked now.",
				"Wait for the build to finish or cancel it, then try again.")
		}
	case corerepos.NotFound:
		writeError(w, http.StatusNotFound, ErrNotFound, "This project or repo isn't tracked (any more).", "Refresh the page and try again.")
	case corerepos.NothingPicked:
		badRequest(w, "No repo was selected.", "Select at least one repo to track.")
	case corerepos.InvalidInput:
		badRequest(w, "The request isn't valid: "+o.Reason+".", "Check the values you sent and try again.")
	default:
		return false
	}
	return true
}

func (s *Server) writeTracked(w http.ResponseWriter, out corerepos.Outcome) {
	if s.trackFailure(w, out) {
		return
	}
	switch o := out.(type) {
	case corerepos.Tracked:
		s.publishRepos()
		status := http.StatusOK
		if o.NewProject {
			status = http.StatusCreated
		}
		writeJSON(w, status, TrackResponse{ProjectID: o.ProjectID, Added: nonNil(o.Added), Restored: nonNil(o.Restored)})
	case corerepos.AlreadyTracked:
		writeJSON(w, http.StatusOK, TrackResponse{ProjectID: o.ProjectID, Added: []core.RepoID{}, Restored: []core.RepoID{}, AlreadyTracked: true})
	default:
		s.log.Error("unexpected tracking outcome", "outcome", fmt.Sprintf("%T", out))
		writeError(w, http.StatusInternalServerError, ErrInternal, "Something went wrong inside Yggdrasil.", "Try again.")
	}
}

func nonNil(ids []core.RepoID) []core.RepoID {
	if ids == nil {
		return []core.RepoID{}
	}
	return ids
}

func (s *Server) handleTrackProject(w http.ResponseWriter, r *http.Request) {
	var req TrackProjectRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := s.tracking.Track(r.Context(), req.Path, req.Name, picksOf(req.Picks), trackingActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeTracked(w, out)
}

func (s *Server) handleTrackRepos(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	var req TrackReposRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := s.tracking.TrackRepos(r.Context(), id, picksOf(req.Picks), trackingActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeTracked(w, out)
}

func (s *Server) handleRescan(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	rep, err := s.tracking.Rescan(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, RescanResponse{New: candidateDTOs(rep.New), Missing: nonNil(rep.Missing)})
}

func (s *Server) writeUntracked(w http.ResponseWriter, out corerepos.Outcome) {
	if s.trackFailure(w, out) {
		return
	}
	done, ok := out.(corerepos.UntrackDone)
	if !ok {
		s.log.Error("unexpected tracking outcome", "outcome", fmt.Sprintf("%T", out))
		writeError(w, http.StatusInternalServerError, ErrInternal, "Something went wrong inside Yggdrasil.", "Try again.")
		return
	}
	if len(done.Repos) > 0 {
		s.publishRepos()
	}
	writeJSON(w, http.StatusOK, UntrackResponse{Repos: nonNil(done.Repos)})
}

func (s *Server) handleUntrackProject(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	out, err := s.tracking.UntrackProject(r.Context(), id, trackingActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeUntracked(w, out)
}

func (s *Server) handleUntrackRepo(w http.ResponseWriter, r *http.Request) {
	id := core.RepoID(r.PathValue("id"))
	if err := id.Validate(); err != nil {
		badRequest(w, "That isn't a repo ID.", "Reload the page and try again.")
		return
	}
	out, err := s.tracking.UntrackRepo(r.Context(), id, trackingActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeUntracked(w, out)
}

func (s *Server) handleRenameProject(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	var req RenameProjectRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := s.tracking.RenameProject(r.Context(), id, req.Name, trackingActor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.trackFailure(w, out) {
		return
	}
	s.publishRepos()
	w.WriteHeader(http.StatusNoContent)
}
