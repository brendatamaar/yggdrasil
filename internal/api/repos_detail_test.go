package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

type fakeDetail struct {
	err    error
	path   repos.RelPath
	side   repos.DiffSide
	limit  repos.Limit
	oid    repos.OID
	called int
}

func (f *fakeDetail) Changes(context.Context, core.RepoID) (git.Status, error) {
	f.called++
	return git.Status{Files: []git.FileChange{{Path: "a.txt", Index: "M"}}, Truncated: true}, f.err
}

func (f *fakeDetail) Diff(_ context.Context, _ core.RepoID, p repos.RelPath, s repos.DiffSide) (repos.DiffResult, error) {
	f.called++
	f.path, f.side = p, s
	return repos.DiffResult{Text: "diff", Binary: true}, f.err
}

func (f *fakeDetail) Log(_ context.Context, _ core.RepoID, l repos.Limit, _ repos.Ref) ([]git.Commit, error) {
	f.called++
	f.limit = l
	return []git.Commit{{Hash: "abcd1234", Short: "abcd", Subject: "s", Date: time.Unix(0, 0).UTC()}}, f.err
}

func (f *fakeDetail) Commit(_ context.Context, _ core.RepoID, o repos.OID) (repos.CommitResult, error) {
	f.called++
	f.oid = o
	return repos.CommitResult{Detail: git.CommitDetail{Commit: git.Commit{Hash: "abcd1234"}, Body: "b",
		Files: []git.CommitFile{{Path: "a.txt", Status: "M"}}}}, f.err
}

func (f *fakeDetail) Branches(context.Context, core.RepoID) ([]git.Branch, error) {
	f.called++
	return []git.Branch{{Name: "main", Current: true}}, f.err
}

func newDetailEnv(t *testing.T, f *fakeDetail) *env {
	t.Helper()
	return newEnv(t, "plain", 0, func(d *Deps) { d.RepoDetail = f })
}

func TestRepoDetailHappyPaths(t *testing.T) {
	f := &fakeDetail{}
	e := newDetailEnv(t, f)
	get := func(path string, out any) {
		t.Helper()
		resp, data := e.do(t, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, data)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	var ch ChangesResponse
	get("/api/repos/r1/changes", &ch)
	if len(ch.Files) != 1 || !ch.Truncated {
		t.Fatalf("changes = %+v", ch)
	}
	var df DiffResponse
	get("/api/repos/r1/diff?path="+url.QueryEscape(`src\a.txt`)+"&mode=staged", &df)
	if f.path != "src/a.txt" || f.side != repos.Staged || !df.Binary {
		t.Fatalf("diff = %+v path=%q side=%q", df, f.path, f.side)
	}
	var lg LogResponse
	get("/api/repos/r1/log", &lg)
	if f.limit != 100 || len(lg.Commits) != 1 {
		t.Fatalf("log limit=%d %+v", f.limit, lg)
	}
	var cd CommitDetailResponse
	get("/api/repos/r1/commits/ABCD1234", &cd)
	if f.oid != "abcd1234" || cd.Body != "b" || len(cd.Files) != 1 {
		t.Fatalf("commit = %+v oid=%q", cd, f.oid)
	}
	var br BranchesResponse
	get("/api/repos/r1/branches", &br)
	if len(br.Branches) != 1 || !br.Branches[0].Current {
		t.Fatalf("branches = %+v", br)
	}
}

func TestRepoDetailRejectsBadInputBeforeTheService(t *testing.T) {
	f := &fakeDetail{}
	e := newDetailEnv(t, f)
	for _, p := range []string{
		"/api/repos/r1/diff?path=..%2Fx",
		"/api/repos/r1/diff?path=-rf",
		"/api/repos/r1/diff?mode=head",
		"/api/repos/r1/log?limit=0",
		"/api/repos/r1/log?limit=501",
		"/api/repos/r1/log?ref=-x",
		"/api/repos/r1/commits/zz",
		"/api/repos/r1/commits/--help",
	} {
		resp, data := e.do(t, http.MethodGet, p, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", p, resp.StatusCode, data)
		}
	}
	if f.called != 0 {
		t.Fatalf("service called %d times for bad input", f.called)
	}
}

func TestRepoDetailMapsErrors(t *testing.T) {
	for cls, want := range map[apperr.Class]int{
		apperr.NotFound: http.StatusNotFound, apperr.Invalid: http.StatusBadRequest, apperr.Unavailable: http.StatusServiceUnavailable,
	} {
		e := newDetailEnv(t, &fakeDetail{err: apperr.New(cls, "msg", nil)})
		for _, p := range []string{"/api/repos/r1/changes", "/api/repos/r1/branches", "/api/repos/r1/commits/abcd"} {
			if resp, data := e.do(t, http.MethodGet, p, nil); resp.StatusCode != want {
				t.Errorf("%s with %v: %d %s, want %d", p, cls, resp.StatusCode, data, want)
			}
		}
	}
}

func TestRepoDetailNotRoutedWithoutService(t *testing.T) {
	e := newEnv(t, "plain", 0)
	if resp, _ := e.do(t, http.MethodGet, "/api/repos/r1/changes", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
