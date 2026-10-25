package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
)

type fakeSearch struct {
	got    []coresearch.Query
	state  coresearch.Search
	err    error
	editor coresearch.Editor
}

func (f *fakeSearch) Search(_ context.Context, q coresearch.Query) (coresearch.Search, error) {
	f.got = append(f.got, q)
	return f.state, f.err
}

func (f *fakeSearch) Editor(context.Context) coresearch.Editor { return f.editor }

func searchState(repoDir string) coresearch.Search {
	repo := coresearch.RepoRef{ID: "rep_a", Name: "shop-api", Path: repoDir}
	gone := coresearch.RepoRef{ID: "rep_b", Name: "shop-web", Path: "x"}
	return coresearch.Search{
		Begun: true,
		Repos: []coresearch.RepoRef{repo, gone},
		Results: []coresearch.Match{
			{Repo: "rep_a", Path: "src/App.java", Line: 4, Col: 9, Text: "needle"},
			{Repo: "rep_a", Path: "../../etc/passwd", Line: 1, Col: 1, Text: "escaped"},
			{Repo: "rep_zzz", Path: "a.txt", Line: 1, Col: 1, Text: "unknown repo"},
		},
		Problems:  map[core.RepoID]coresearch.ProblemKind{"rep_b": coresearch.ProblemMissing},
		Truncated: true,
	}
}

func TestSearchEndpoint(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSearch{state: searchState(dir), editor: coresearch.EditorVSCode}
	e := newEnv(t, "plain", 0, func(d *Deps) { d.Search = f })

	resp, data := e.do(t, http.MethodPost, "/api/search", SearchRequest{Pattern: "needle", Paths: []string{"*.java"}})
	var body SearchResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &body) != nil {
		t.Fatalf("POST /api/search = %d %s", resp.StatusCode, data)
	}
	if len(f.got) != 1 || f.got[0].Pattern != "needle" || f.got[0].Mode != coresearch.ModeLiteral || f.got[0].Paths[0] != "*.java" {
		t.Fatalf("query = %+v", f.got)
	}
	// The match that leaves its repo and the one of an unknown repo are dropped.
	if len(body.Matches) != 1 || body.Matches[0].RepoName != "shop-api" || !body.Truncated {
		t.Fatalf("body = %+v", body)
	}
	want, _ := coresearch.EditorURL(coresearch.EditorVSCode, filepath.Join(dir, "src", "App.java"), 4, 9)
	if body.Matches[0].EditorURL != want || !strings.HasPrefix(want, "vscode://file/") {
		t.Errorf("editor url = %q, want %q", body.Matches[0].EditorURL, want)
	}
	// The dropped match (a path that leaves its repo) is reported for shop-api,
	// next to shop-web's own failure.
	byKind := map[string]string{}
	for _, p := range body.Problems {
		if p.Message == "" {
			t.Errorf("problem without a message: %+v", p)
		}
		byKind[p.Kind] = p.RepoName
	}
	if len(body.Problems) != 2 || byKind["missing"] != "shop-web" || byKind["paths_skipped"] != "shop-api" {
		t.Errorf("problems = %+v", body.Problems)
	}

	f.editor = ""
	_, data = e.do(t, http.MethodPost, "/api/search", SearchRequest{Pattern: "needle"})
	if strings.Contains(string(data), "editorUrl") {
		t.Errorf("a link without an editor: %s", data)
	}
}

func TestSearchEndpointRejectsBadRequests(t *testing.T) {
	f := &fakeSearch{}
	e := newEnv(t, "plain", 0, func(d *Deps) { d.Search = f })
	for name, body := range map[string]any{
		"empty":         SearchRequest{},
		"bad regex":     SearchRequest{Pattern: "(a", Mode: "regex"},
		"dash glob":     SearchRequest{Pattern: "a", Paths: []string{"-x"}},
		"parent glob":   SearchRequest{Pattern: "a", Paths: []string{"../x"}},
		"unknown field": map[string]any{"pattern": "a", "path": "C:/secret"},
	} {
		resp, data := e.do(t, http.MethodPost, "/api/search", body)
		if resp.StatusCode != 400 {
			t.Errorf("%s: status %d, %s", name, resp.StatusCode, data)
		}
	}
	if len(f.got) != 0 {
		t.Fatalf("the service ran for a rejected request: %+v", f.got)
	}
}

func TestSearchEndpointMapsServiceErrors(t *testing.T) {
	f := &fakeSearch{err: apperr.New(apperr.Invalid, "One of the selected repos is not tracked.", nil)}
	e := newEnv(t, "plain", 0, func(d *Deps) { d.Search = f })
	resp, data := e.do(t, http.MethodPost, "/api/search", SearchRequest{Pattern: "a", Repos: []core.RepoID{"rep_x"}})
	if resp.StatusCode != 400 || !strings.Contains(string(data), "not tracked") {
		t.Fatalf("status %d, %s", resp.StatusCode, data)
	}
}

func TestSearchRouteIsAbsentWithoutService(t *testing.T) {
	e := newEnv(t, "plain", 0)
	resp, _ := e.do(t, http.MethodPost, "/api/search", SearchRequest{Pattern: "a"})
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestJoinInside(t *testing.T) {
	dir := t.TempDir()
	if p, ok := joinInside(dir, "a/b.txt"); !ok || p != filepath.Join(dir, "a", "b.txt") {
		t.Errorf("got %q, %v", p, ok)
	}
	for _, rel := range []string{"", "../x", "a/../../x", "/abs", `a\b`, "C:/x", "a/../.."} {
		if p, ok := joinInside(dir, rel); ok {
			t.Errorf("%q accepted as %q", rel, p)
		}
	}
	if _, ok := joinInside("", "a.txt"); ok {
		t.Error("an empty repo folder was accepted")
	}
}
