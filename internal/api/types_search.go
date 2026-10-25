package api

import "github.com/brendatamaar/yggdrasil/internal/core"

// DTOs of code search (XR-01, docs/design/code-search.md): POST /api/search.
// One response within 15 s; there is no streaming at v1.

// SearchRequest is the body of POST /api/search. Mode is "literal" (default)
// or "regex". Case means case-sensitive. Repos limits the search to those
// repos (empty means every active repo in scope). Paths are pathspec globs
// relative to each repo, for example "*.java".
type SearchRequest struct {
	Pattern string        `json:"pattern"`
	Mode    string        `json:"mode,omitempty"`
	Case    bool          `json:"case,omitempty"`
	Repos   []core.RepoID `json:"repos,omitempty"`
	Paths   []string      `json:"paths,omitempty"`
}

// SearchMatchDTO is one matching line. Text is at most 300 characters.
// EditorURL opens the file at the match in the user's editor; it is empty when
// no editor is configured.
type SearchMatchDTO struct {
	Repo      core.RepoID `json:"repo"`
	RepoName  string      `json:"repoName"`
	Path      string      `json:"path"`
	Line      int         `json:"line"`
	Col       int         `json:"col"`
	Text      string      `json:"text"`
	EditorURL string      `json:"editorUrl,omitempty"`
}

// SearchProblemDTO says why a repo could not be searched. Kind is
// "timed_out", "git_failed" or "missing". Message says what to do next.
type SearchProblemDTO struct {
	Repo     core.RepoID `json:"repo"`
	RepoName string      `json:"repoName"`
	Kind     string      `json:"kind"`
	Message  string      `json:"message"`
}

// SearchResponse is the 200 body of POST /api/search. Truncated is true when
// a cap (200 per repo, 1,000 in total) cut some matches.
type SearchResponse struct {
	Matches   []SearchMatchDTO   `json:"matches"`
	Problems  []SearchProblemDTO `json:"problems"`
	Truncated bool               `json:"truncated"`
}
