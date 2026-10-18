package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

// fakeStatus is a StatusService with a fixed state.
type fakeStatus struct {
	mu      sync.Mutex
	state   corerepos.Refresher
	asked   []askedRefresh
	snapErr error
	refErr  error
}

type askedRefresh struct {
	ids    []core.RepoID
	reason corerepos.RefreshReason
}

func (f *fakeStatus) Snapshot(context.Context) (corerepos.Refresher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.snapErr
}

func (f *fakeStatus) Refresh(_ context.Context, ids []core.RepoID, reason corerepos.RefreshReason) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, askedRefresh{ids, reason})
	return f.refErr
}

// failingHosts answers only FailedPipelines; the embedded nil interface makes
// any other call panic, which the test would show.
type failingHosts struct {
	HostsService
	failed map[core.RepoID]struct{}
	err    error
}

func (h failingHosts) FailedPipelines(context.Context) (map[core.RepoID]struct{}, error) {
	return h.failed, h.err
}

var statusAt = time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)

func newStatusEnv(t *testing.T, st *fakeStatus, hosts HostsService) *env {
	t.Helper()
	return newEnv(t, "plain", 0, func(d *Deps) {
		d.Status = st
		d.Hosts = hosts
	})
}

func (e *env) getRepos(t *testing.T, query string) (int, ReposResponse, []byte) {
	t.Helper()
	resp, data := e.do(t, http.MethodGet, "/api/repos"+query, nil)
	var out ReposResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
	}
	return resp.StatusCode, out, data
}

func stateWith(e *env, v corerepos.View, inFlight bool) corerepos.Refresher {
	r := corerepos.NewRefresher(8)
	r.Entries[e.repo.ID] = corerepos.Entry{Path: corerepos.CleanPath(e.repo.Path), Attempt: 1, View: v}
	if inFlight {
		r.InFlight[e.repo.ID] = 2
	}
	return r
}

func TestListReposUnknownView(t *testing.T) {
	st := &fakeStatus{state: corerepos.NewRefresher(8)}
	e := newStatusEnv(t, st, nil)
	code, out, data := e.getRepos(t, "")
	if code != http.StatusOK || len(out.Projects) != 1 || len(out.Projects[0].Repos) != 1 {
		t.Fatalf("GET /api/repos = %d %s", code, data)
	}
	r := out.Projects[0].Repos[0]
	if r.ID != e.repo.ID || r.View != RepoViewUnknown || r.Refreshing || r.CheckedAt != nil || r.Flags == nil || len(r.Flags) != 0 {
		t.Errorf("repo = %+v", r)
	}
	if out.Counts.All != 1 || out.Projects[0].Counts.All != 1 {
		t.Errorf("counts = %+v / %+v", out.Counts, out.Projects[0].Counts)
	}
}

func TestListReposFreshFiltersAndCounts(t *testing.T) {
	st := &fakeStatus{}
	e := newStatusEnv(t, st, failingHosts{failed: map[core.RepoID]struct{}{}})
	st.state = stateWith(e, corerepos.Fresh{At: statusAt, Snap: corerepos.Snapshot{
		Branch: "main", Upstream: "origin/main", HasUpstream: true, Behind: 2, Unstaged: 3,
		LastCommit: &corerepos.CommitRef{OID: "abc", Subject: "fix cart", Author: "Ada", At: statusAt},
	}}, true)
	code, out, data := e.getRepos(t, "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, data)
	}
	r := out.Projects[0].Repos[0]
	if r.View != RepoViewFresh || !r.Refreshing || r.Branch != "main" || r.Behind != 2 || r.Unstaged != 3 ||
		r.CheckedAt == nil || !r.CheckedAt.Equal(statusAt) || r.LastCommit == nil || r.LastCommit.Subject != "fix cart" {
		t.Errorf("repo = %+v", r)
	}
	if want := (RepoCountsDTO{All: 1, Uncommitted: 1, Behind: 1}); out.Counts != want {
		t.Errorf("counts = %+v, want %+v", out.Counts, want)
	}
	for filter, wantRepos := range map[string]int{"all": 1, "uncommitted": 1, "behind": 1, "ahead": 0, "problems": 0, "failed_pipeline": 0} {
		_, got, _ := e.getRepos(t, "?filter="+filter)
		if n := len(got.Projects[0].Repos); n != wantRepos {
			t.Errorf("filter %s: %d repos, want %d", filter, n, wantRepos)
		}
		if got.Counts.All != 1 || got.Projects[0].Counts.Behind != 1 {
			t.Errorf("filter %s changed the counts: %+v", filter, got.Counts)
		}
	}
	if _, got, _ := e.getRepos(t, "?project="+string(e.project.ID)); len(got.Projects) != 1 {
		t.Errorf("project filter: %d projects", len(got.Projects))
	}
}

func TestListReposProblemAndFailedPipeline(t *testing.T) {
	st := &fakeStatus{}
	hosts := failingHosts{}
	e := newStatusEnv(t, st, hosts)
	st.state = stateWith(e, corerepos.Problem{Kind: corerepos.ProblemTimedOut, At: statusAt, Last: &corerepos.Snapshot{Branch: "main", HasUpstream: true, Ahead: 4}}, false)
	e.srv.hosts = failingHosts{failed: map[core.RepoID]struct{}{e.repo.ID: {}}}
	_, out, _ := e.getRepos(t, "?filter=problems")
	if len(out.Projects[0].Repos) != 1 {
		t.Fatalf("problems filter: %+v", out.Projects[0].Repos)
	}
	r := out.Projects[0].Repos[0]
	if r.View != RepoViewProblem || r.Problem != "timed_out" || r.ProblemMessage == "" || r.Ahead != 4 || r.Branch != "main" ||
		len(r.Flags) != 1 || r.Flags[0] != "timed_out" || !r.FailedPipeline {
		t.Errorf("repo = %+v", r)
	}
	if want := (RepoCountsDTO{All: 1, Ahead: 1, FailedPipeline: 1, Problems: 1}); out.Counts != want {
		t.Errorf("counts = %+v, want %+v", out.Counts, want)
	}
	_, out, _ = e.getRepos(t, "?filter=failed_pipeline")
	if len(out.Projects[0].Repos) != 1 {
		t.Errorf("failed_pipeline filter: %+v", out.Projects[0].Repos)
	}
}

func TestListReposHostsFailureIsNotAnError(t *testing.T) {
	st := &fakeStatus{state: corerepos.NewRefresher(8)}
	e := newStatusEnv(t, st, failingHosts{err: errors.New("host is down")})
	if code, _, data := e.getRepos(t, ""); code != http.StatusOK {
		t.Fatalf("status %d: %s", code, data)
	}
}

func TestListReposRejections(t *testing.T) {
	st := &fakeStatus{state: corerepos.NewRefresher(8)}
	e := newStatusEnv(t, st, nil)
	for query, want := range map[string]int{
		"?filter=everything": http.StatusBadRequest,
		"?project=prj_nope":  http.StatusNotFound,
		"?project=a%20b":     http.StatusBadRequest,
	} {
		if code, _, data := e.getRepos(t, query); code != want {
			t.Errorf("GET /api/repos%s = %d %s, want %d", query, code, data, want)
		}
	}
	st.snapErr = apperr.New(apperr.Unavailable, "Repo status is not running.", nil)
	if code, _, _ := e.getRepos(t, ""); code != http.StatusServiceUnavailable {
		t.Errorf("status service down: %d, want 503", code)
	}
}

func TestRefreshRepos(t *testing.T) {
	st := &fakeStatus{state: corerepos.NewRefresher(8)}
	e := newStatusEnv(t, st, nil)
	resp, data := e.do(t, http.MethodPost, "/api/repos/refresh", RefreshReposRequest{IDs: []core.RepoID{e.repo.ID}, Reason: "focus"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("refresh = %d %s", resp.StatusCode, data)
	}
	resp, _ = e.do(t, http.MethodPost, "/api/repos/refresh", RefreshReposRequest{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("empty refresh = %d", resp.StatusCode)
	}
	st.mu.Lock()
	asked := st.asked
	st.mu.Unlock()
	if len(asked) != 2 || asked[0].reason != corerepos.ReasonFocus || len(asked[0].ids) != 1 || asked[1].reason != corerepos.ReasonUser || len(asked[1].ids) != 0 {
		t.Errorf("service asked %+v", asked)
	}
	bad := []any{
		RefreshReposRequest{Reason: "whenever"},
		RefreshReposRequest{IDs: []core.RepoID{"rep bad"}},
		map[string]any{"ids": []string{}, "surprise": true},
	}
	for _, body := range bad {
		if resp, data := e.do(t, http.MethodPost, "/api/repos/refresh", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %v = %d %s, want 400", body, resp.StatusCode, data)
		}
	}
	st.refErr = apperr.New(apperr.Unavailable, "Repo status is not running.", nil)
	if resp, _ := e.do(t, http.MethodPost, "/api/repos/refresh", RefreshReposRequest{}); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status service down: %d, want 503", resp.StatusCode)
	}
}

func TestStatusRoutesNeedTheService(t *testing.T) {
	e := newEnv(t, "plain", 0)
	if resp, _ := e.do(t, http.MethodPost, "/api/repos/refresh", RefreshReposRequest{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unwired refresh = %d, want 404", resp.StatusCode)
	}
	e.srv.PublishRepoStatus(e.repo.ID) // must not panic without a service
	e.srv.PublishRepoCounts()
}

func TestStatusPublishing(t *testing.T) {
	st := &fakeStatus{}
	e := newStatusEnv(t, st, nil)
	st.state = stateWith(e, corerepos.Fresh{At: statusAt, Snap: corerepos.Snapshot{Branch: "dev", Ahead: 1}}, false)
	ws := e.dial(t)
	subscribe(t, ws, repoTopic{ID: e.repo.ID}.String())
	subscribe(t, ws, reposTopic{}.String())
	time.Sleep(150 * time.Millisecond) // let the subscriptions register

	e.srv.PublishRepoStatus(e.repo.ID)
	m, err := readMsg(t, ws, 5*time.Second)
	if err != nil || m.Topic != "repo:"+string(e.repo.ID) {
		t.Fatalf("repo message = %+v, %v", m, err)
	}
	var dto RepoStatusDTO
	if err := json.Unmarshal(m.Data, &dto); err != nil || dto.Branch != "dev" || dto.Ahead != 1 || dto.ID != e.repo.ID {
		t.Errorf("repo data = %+v, %v", dto, err)
	}

	e.srv.PublishRepoCounts()
	m, err = readMsg(t, ws, 5*time.Second)
	if err != nil || m.Topic != TopicRepos {
		t.Fatalf("counts message = %+v, %v", m, err)
	}
	var counts ReposChangedMessage
	if err := json.Unmarshal(m.Data, &counts); err != nil || counts.Counts.All != 1 || counts.Counts.Ahead != 1 {
		t.Errorf("counts data = %+v, %v", counts, err)
	}

	// A repo that is no longer tracked publishes nothing on its topic.
	e.srv.PublishRepoStatus("rep_gone")
	if m, err := readMsg(t, ws, 300*time.Millisecond); err == nil {
		t.Errorf("unexpected message %+v", m)
	}
}
