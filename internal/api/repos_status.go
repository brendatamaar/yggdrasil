package api

import (
	"context"
	"net/http"
	"strings"
	"path/filepath"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// This file is owned by the repo-status task (WS-02 to WS-04): the status
// service interface, GET /api/repos (the joined read model of projects,
// status and failed pipelines) and POST /api/repos/refresh. The DTOs are in
// types_repos.go. The file is excluded from tygo (tygo.yaml).

// StatusService is implemented by *repos.StatusService
// (docs/design/repo-status.md). The status is an in-memory read model.
type StatusService interface {
	// Snapshot returns a consistent copy of the status of every tracked repo
	// (Entries) and of the reads in flight (InFlight).
	Snapshot(ctx context.Context) (corerepos.Refresher, error)
	// Refresh asks for a new read of the repos; empty ids means all.
	Refresh(ctx context.Context, ids []core.RepoID, reason corerepos.RefreshReason) error
}

// failedPipelineSource is the part of HostsService the read model uses. A nil
// source means no failed pipelines.
type failedPipelineSource interface {
	FailedPipelines(ctx context.Context) (map[core.RepoID]struct{}, error)
}

// routesReposStatus registers the status endpoints. It registers nothing
// when the status service is not wired.
func (s *Server) routesReposStatus(mux *http.ServeMux) {
	if s.status == nil {
		return
	}
	mux.HandleFunc("GET /api/repos", s.listRepoStatus)
	mux.HandleFunc("POST /api/repos/refresh", s.refreshRepos)
}

// PublishRepoStatus sends the status of one repo on its "repo:<id>" topic.
// It is the status service's Publisher.RepoChanged. A repo that is no longer
// tracked sends nothing: the "repos" message tells clients to refetch.
func (s *Server) PublishRepoStatus(id core.RepoID) {
	if s.status == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	repo, err := s.store.GetRepo(ctx, id)
	if err != nil {
		return
	}
	project, err := s.store.GetProject(ctx, repo.ProjectID)
	if err != nil {
		return
	}
	snap, err := s.status.Snapshot(ctx)
	if err != nil {
		return
	}
	failed := s.failedPipelines(ctx)
	_, fp := failed[id]
	s.hub.publishNext(repoTopic{ID: id}, repoStatusDTO(project, repo, snap, fp))
}

// PublishRepoCounts sends the counts on the "repos" topic. It is the status
// service's Publisher.CountsChanged.
func (s *Server) PublishRepoCounts() {
	if s.status == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	snap, err := s.status.Snapshot(ctx)
	if err != nil {
		return
	}
	counts := corerepos.CountEntries(snap.Entries, s.failedPipelines(ctx))
	s.hub.publishNext(reposTopic{}, ReposChangedMessage{Counts: countsDTO(counts)})
}

// failedPipelines returns the repos whose pipeline failed. Git hosts being
// unwired or failing is not an error for the status: the filter is empty.
func (s *Server) failedPipelines(ctx context.Context) map[core.RepoID]struct{} {
	var src failedPipelineSource
	if s.hosts != nil {
		src = s.hosts
	}
	if src == nil {
		return nil
	}
	m, err := src.FailedPipelines(ctx)
	if err != nil {
		s.log.Warn("could not read failed pipelines", "err", err)
		return nil
	}
	return m
}

func (s *Server) listRepoStatus(w http.ResponseWriter, r *http.Request) {
	filter, err := repos.ParseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	only := core.ProjectID(r.URL.Query().Get("project"))
	if only != "" {
		if err := only.Validate(); err != nil {
			badRequest(w, "The project ID is not valid.", "Check the values you sent and try again.")
			return
		}
	}
	ctx := r.Context()
	snap, err := s.status.Snapshot(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	failed := s.failedPipelines(ctx)
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := ReposResponse{Projects: []ProjectDTO{}}
	all := map[core.RepoID]corerepos.Entry{}
	for _, p := range projects {
		if only != "" && p.ID != only {
			continue
		}
		rows, err := s.store.ListRepos(ctx, p.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		dto := ProjectDTO{ID: p.ID, Name: p.Name, Path: p.Path, Repos: []RepoStatusDTO{}}
		mine := map[core.RepoID]corerepos.Entry{}
		for _, repo := range rows {
			e, known := snap.Entries[repo.ID]
			if !known {
				e = corerepos.Entry{View: corerepos.Unknown{}}
			}
			mine[repo.ID], all[repo.ID] = e, e
			_, fp := failed[repo.ID]
			if filter.Matches(e.View, fp) {
				dto.Repos = append(dto.Repos, repoStatusDTO(p, repo, snap, fp))
			}
		}
		dto.Counts = countsDTO(corerepos.CountEntries(mine, failed))
		resp.Projects = append(resp.Projects, dto)
	}
	if only != "" && len(resp.Projects) == 0 {
		writeError(w, http.StatusNotFound, ErrNotFound, "That project isn't tracked.", "Refresh the page and try again.")
		return
	}
	resp.Counts = countsDTO(corerepos.CountEntries(all, failed))
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) refreshRepos(w http.ResponseWriter, r *http.Request) {
	var body RefreshReposRequest
	if !decodeBody(w, r, &body) {
		return
	}
	ids := make([]string, len(body.IDs))
	for i, id := range body.IDs {
		ids[i] = string(id)
	}
	req, err := repos.ParseRefresh(ids, body.Reason, s.clock())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.status.Refresh(r.Context(), req.IDs, req.Reason); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct{}{})
}

func countsDTO(c corerepos.Counts) RepoCountsDTO {
	return RepoCountsDTO{
		All: c.All, Uncommitted: c.Uncommitted, Behind: c.Behind, Ahead: c.Ahead,
		FailedPipeline: c.FailedPipeline, Problems: c.Problems,
	}
}

// repoStatusDTO joins a repo row with its status entry.
func repoStatusDTO(p core.Project, repo core.Repo, snap corerepos.Refresher, failedPipeline bool) RepoStatusDTO {
	e, known := snap.Entries[repo.ID]
	var view corerepos.View = corerepos.Unknown{}
	if known {
		view = e.View
	}
	_, refreshing := snap.InFlight[repo.ID]
	rel := "."
	if rp, err := filepath.Rel(p.Path, repo.Path); err == nil && rp != ".." && !strings.HasPrefix(rp, ".."+string(filepath.Separator)) {
		rel = filepath.ToSlash(rp)
	}
	dto := RepoStatusDTO{
		ID: repo.ID, ProjectID: repo.ProjectID, Name: repo.Name, RelPath: rel, Path: repo.Path,
		Trusted: repo.Trusted, Refreshing: refreshing, View: RepoViewUnknown,
		Flags: []string{}, FailedPipeline: failedPipeline,
	}
	for _, f := range corerepos.Flags(view) {
		dto.Flags = append(dto.Flags, string(f))
	}
	if at, ok := corerepos.ViewAt(view); ok {
		dto.CheckedAt = &at
	}
	switch v := view.(type) {
	case corerepos.Fresh:
		dto.View = RepoViewFresh
	case corerepos.Problem:
		dto.View = RepoViewProblem
		dto.Problem = string(v.Kind)
		dto.ProblemMessage = problemMessage(v.Kind)
	}
	if sn, ok := corerepos.SnapshotOf(view); ok {
		dto.Branch, dto.Detached, dto.Upstream, dto.HasUpstream = sn.Branch, sn.Detached, sn.Upstream, sn.HasUpstream
		dto.Ahead, dto.Behind = sn.Ahead, sn.Behind
		dto.Staged, dto.Unstaged, dto.Untracked, dto.Conflicted = sn.Staged, sn.Unstaged, sn.Untracked, sn.Conflicted
		if sn.LastCommit != nil {
			dto.LastCommit = &CommitRefDTO{OID: sn.LastCommit.OID, Subject: sn.LastCommit.Subject, Author: sn.LastCommit.Author, At: sn.LastCommit.At}
		}
	}
	return dto
}

// problemMessage says what happened and what to do next. It holds no paths
// or git output.
func problemMessage(k corerepos.ProblemKind) string {
	switch k {
	case corerepos.ProblemMissing:
		return "The folder is gone. Restore it, or stop tracking this repo."
	case corerepos.ProblemNotARepo:
		return "This folder is no longer a git repository. Restore its .git folder, or stop tracking this repo."
	case corerepos.ProblemTimedOut:
		return "Git took too long to answer. Check for antivirus scans or a slow network drive; Yggdrasil will try again."
	case corerepos.ProblemGitFailed:
		return "Git could not read this repo. Another git command may be running; Yggdrasil will try again."
	}
	return ""
}

var _ StatusService = (*repos.StatusService)(nil)
