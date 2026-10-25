package api

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/search"
)

// Code search (XR-01, docs/design/code-search.md). The file is excluded from
// tygo (tygo.yaml): the DTOs are in types_search.go.

// SearchService is implemented by internal/search. It searches only active
// tracked repos (invariant 6) and reads only: no audit entry. Errors are
// classified by internal/apperr.
type SearchService interface {
	// Search runs a parsed query and returns the final core state: results,
	// per-repo problems and the repos searched.
	Search(ctx context.Context, q coresearch.Query) (coresearch.Search, error)
	// Editor is the editor for links, or "" when none is set.
	Editor(ctx context.Context) coresearch.Editor
}

// routesSearch registers the search endpoint. It registers nothing when the
// service is not wired.
func (s *Server) routesSearch(mux *http.ServeMux) {
	if s.search == nil {
		return
	}
	mux.HandleFunc("POST /api/search", s.handleSearch)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	raw := coresearch.RawQuery{Pattern: req.Pattern, Mode: req.Mode, Case: req.Case, Paths: req.Paths}
	for _, id := range req.Repos {
		raw.Repos = append(raw.Repos, string(id))
	}
	q, err := search.ParseRequest(raw)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	state, err := s.search.Search(r.Context(), q)
	if err != nil {
		if r.Context().Err() != nil {
			return // the client went away
		}
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, searchResponse(state, s.search.Editor(r.Context())))
}

// searchResponse builds the body. A match whose path would leave its repo is
// dropped (the shell already refuses such paths; this is the second check
// before a path becomes an editor link).
func searchResponse(st coresearch.Search, editor coresearch.Editor) SearchResponse {
	repos := make(map[core.RepoID]coresearch.RepoRef, len(st.Repos))
	for _, r := range st.Repos {
		repos[r.ID] = r
	}
	out := SearchResponse{Matches: []SearchMatchDTO{}, Problems: []SearchProblemDTO{}, Truncated: st.Truncated}
	dropped := map[core.RepoID]bool{} // repos with a match that could not be shown
	for _, m := range st.Results {
		repo, ok := repos[m.Repo]
		abs, inside := joinInside(repo.Path, m.Path)
		if !ok || !inside {
			if ok {
				dropped[m.Repo] = true
			}
			continue
		}
		dto := SearchMatchDTO{Repo: m.Repo, RepoName: repo.Name, Path: m.Path, Line: m.Line, Col: m.Col, Text: m.Text}
		if editor != "" {
			if u, err := coresearch.EditorURL(editor, abs, m.Line, m.Col); err == nil {
				dto.EditorURL = u
			}
		}
		out.Matches = append(out.Matches, dto)
	}
	for id, kind := range st.Problems {
		out.Problems = append(out.Problems, SearchProblemDTO{
			Repo: id, RepoName: repos[id].Name, Kind: string(kind), Message: searchProblemMessage(kind),
		})
	}
	for id := range dropped {
		if _, has := st.Problems[id]; has {
			continue // the repo is already flagged
		}
		out.Problems = append(out.Problems, SearchProblemDTO{
			Repo: id, RepoName: repos[id].Name, Kind: "paths_skipped",
			Message: "Some matches in this repo were left out because their file names cannot be opened safely on this system.",
		})
	}
	slices.SortFunc(out.Problems, func(a, b SearchProblemDTO) int {
		if c := strings.Compare(a.RepoName, b.RepoName); c != 0 {
			return c
		}
		return strings.Compare(string(a.Repo), string(b.Repo))
	})
	return out
}

// joinInside joins a repo-relative "/" path to the repo folder and reports
// whether the result stays inside it.
func joinInside(repoDir, rel string) (string, bool) {
	if repoDir == "" || rel == "" || strings.ContainsRune(rel, '\\') || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", false
	}
	return filepath.Join(repoDir, filepath.FromSlash(rel)), true
}

func searchProblemMessage(k coresearch.ProblemKind) string {
	switch k {
	case coresearch.ProblemTimedOut:
		return "This repo took too long to search, so it is not in the results. Narrow the search with a path filter or pick fewer repos."
	case coresearch.ProblemMissing:
		return "The repo folder is missing. Restore it or untrack the repo."
	default:
		return "Git could not search this repo. Check that the folder is a healthy git repository and try again."
	}
}
