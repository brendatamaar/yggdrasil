package api

import (
	"context"
	"net/http"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// This file holds the read-only endpoints of the repo page (WS-05). They are
// stateless reads: no step, no audit. The DTOs are in types_repos_detail.go.
// The file is excluded from tygo (tygo.yaml).

// RepoDetailService is implemented by *repos.DetailService. Every method
// fails with an apperr NotFound when the repo is not tracked.
type RepoDetailService interface {
	Changes(ctx context.Context, id core.RepoID) (git.Status, error)
	Diff(ctx context.Context, id core.RepoID, path repos.RelPath, side repos.DiffSide) (repos.DiffResult, error)
	Log(ctx context.Context, id core.RepoID, limit repos.Limit, ref repos.Ref) ([]git.Commit, error)
	Commit(ctx context.Context, id core.RepoID, oid repos.OID) (repos.CommitResult, error)
	Branches(ctx context.Context, id core.RepoID) ([]git.Branch, error)
}

// routesReposDetail registers nothing when the service is not wired.
func (s *Server) routesReposDetail(mux *http.ServeMux) {
	if s.repoDetail == nil {
		return
	}
	mux.HandleFunc("GET /api/repos/{id}/changes", s.repoChanges)
	mux.HandleFunc("GET /api/repos/{id}/diff", s.repoDiff)
	mux.HandleFunc("GET /api/repos/{id}/log", s.repoLog)
	mux.HandleFunc("GET /api/repos/{id}/commits/{oid}", s.repoCommit)
	mux.HandleFunc("GET /api/repos/{id}/branches", s.repoBranches)
}

// detailRepoID parses the {id} segment. A malformed ID cannot be tracked, so
// it is a 404.
func (s *Server) detailRepoID(w http.ResponseWriter, r *http.Request) (core.RepoID, bool) {
	id := core.RepoID(r.PathValue("id"))
	if err := id.Validate(); err != nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "That repo isn't tracked.", "Refresh the page and try again.")
		return "", false
	}
	return id, true
}

func (s *Server) repoChanges(w http.ResponseWriter, r *http.Request) {
	id, ok := s.detailRepoID(w, r)
	if !ok {
		return
	}
	st, err := s.repoDetail.Changes(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := ChangesResponse{Files: make([]FileChangeDTO, 0, len(st.Files)), Truncated: st.Truncated}
	for _, f := range st.Files {
		resp.Files = append(resp.Files, FileChangeDTO{Path: f.Path, OrigPath: f.OrigPath, Index: f.Index, Worktree: f.Worktree})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) repoDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := s.detailRepoID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	path, err := repos.ParseRelPath(q.Get("path"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	side, err := repos.ParseDiffSide(q.Get("mode"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.repoDetail.Diff(r.Context(), id, path, side)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, diffDTO(res))
}

func (s *Server) repoLog(w http.ResponseWriter, r *http.Request) {
	id, ok := s.detailRepoID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, err := repos.ParseLimit(q.Get("limit"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ref, err := repos.ParseRef(q.Get("ref"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cs, err := s.repoDetail.Log(r.Context(), id, limit, ref)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := LogResponse{Commits: make([]CommitSummaryDTO, 0, len(cs))}
	for _, c := range cs {
		resp.Commits = append(resp.Commits, commitDTO(c))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) repoCommit(w http.ResponseWriter, r *http.Request) {
	id, ok := s.detailRepoID(w, r)
	if !ok {
		return
	}
	oid, err := repos.ParseOID(r.PathValue("oid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.repoDetail.Commit(r.Context(), id, oid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := CommitDetailResponse{Commit: commitDTO(res.Detail.Commit), Body: res.Detail.Body,
		Files: make([]CommitFileDTO, 0, len(res.Detail.Files)), Diff: diffDTO(res.Diff)}
	for _, f := range res.Detail.Files {
		resp.Files = append(resp.Files, CommitFileDTO{Path: f.Path, OrigPath: f.OrigPath, Status: f.Status})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) repoBranches(w http.ResponseWriter, r *http.Request) {
	id, ok := s.detailRepoID(w, r)
	if !ok {
		return
	}
	bs, err := s.repoDetail.Branches(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := BranchesResponse{Branches: make([]BranchDTO, 0, len(bs))}
	for _, b := range bs {
		resp.Branches = append(resp.Branches, BranchDTO{Name: b.Name, Remote: b.Remote, Current: b.Current, Upstream: b.Upstream, OID: b.OID})
	}
	writeJSON(w, http.StatusOK, resp)
}

func diffDTO(d repos.DiffResult) DiffResponse {
	return DiffResponse{Text: d.Text, Truncated: d.Truncated, Binary: d.Binary}
}

func commitDTO(c git.Commit) CommitSummaryDTO {
	p := c.Parents
	if p == nil {
		p = []string{}
	}
	return CommitSummaryDTO{OID: c.Hash, Short: c.Short, Subject: c.Subject, Author: c.Author, Email: c.Email, At: c.Date, Parents: p}
}

var _ RepoDetailService = (*repos.DetailService)(nil)
