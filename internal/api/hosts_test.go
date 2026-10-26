package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

type fakeHosts struct {
	refreshed  []corehosts.Key
	refreshErr error
	views      map[core.RepoID]RepoHostView
}

var hostsAt = time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

func (f *fakeHosts) Instances(context.Context) ([]corehosts.Instance, error) {
	return []corehosts.Instance{
		{ID: "git.acme.dev", Kind: corehosts.KindGitLab, Base: "https://git.acme.dev", Token: true},
		{ID: "github.com", Kind: corehosts.KindGitHub, Base: "https://github.com"},
	}, nil
}

func (f *fakeHosts) ConfigInfo(context.Context) HostsConfigInfo {
	return HostsConfigInfo{EnvPath: "data/.env", Problems: []string{"line 3 (YGG_HOST_2): the kind must be gitlab, github or gitea"}}
}

func (f *fakeHosts) RepoHost(_ context.Context, id core.RepoID) (RepoHostView, error) {
	if v, ok := f.views[id]; ok {
		return v, nil
	}
	return RepoHostView{}, apperr.New(apperr.NotFound, "That repo isn't tracked.", nil)
}

func (f *fakeHosts) Inbox(context.Context) (InboxView, error) {
	mr := corehosts.MergeRequest{Number: 7, Title: "Fix totals rounding", URL: "https://git.acme.dev/acme-shop/shop-web/-/merge_requests/7", Author: "jonas", State: corehosts.MROpen, UpdatedAt: hostsAt}
	retry := hostsAt.Add(time.Minute)
	return InboxView{
		Items: []InboxEntry{{Instance: "git.acme.dev", Item: corehosts.InboxItem{MR: mr, Repo: "acme-shop/shop-web", Role: corehosts.RoleReviewRequested}}},
		Instances: []InboxInstance{
			{Instance: "git.acme.dev", Entry: corehosts.Entry{Phase: corehosts.Idle{FetchedAt: hostsAt}}},
			{Instance: "code.example.org", Entry: corehosts.Entry{Phase: corehosts.Waiting{Until: retry, Why: corehosts.WaitRateLimited}}},
		},
	}, nil
}

func (f *fakeHosts) Refresh(_ context.Context, keys []corehosts.Key) error {
	f.refreshed = keys
	return f.refreshErr
}

func (f *fakeHosts) FailedPipelines(context.Context) (map[core.RepoID]struct{}, error) {
	return nil, nil
}

func hostsEnv(t *testing.T, f *fakeHosts) *env {
	return newEnv(t, func(d *Deps) { d.Hosts = f })
}

func TestGetHostsReportsTokenFlagOnly(t *testing.T) {
	e := hostsEnv(t, &fakeHosts{})
	resp, data := e.do(t, http.MethodGet, "/api/hosts", nil)
	var body HostsResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &body) != nil {
		t.Fatalf("GET /api/hosts = %d %s", resp.StatusCode, data)
	}
	if len(body.Instances) != 2 || !body.Instances[0].TokenConfigured || body.Instances[1].TokenConfigured ||
		body.Instances[0].Kind != HostKindGitLab || body.EnvPath != "data/.env" || len(body.ConfigProblems) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if strings.Contains(strings.ToLower(string(data)), "token\":\"") {
		t.Errorf("the response has a token field: %s", data)
	}
}

func TestGetRepoHostPhases(t *testing.T) {
	inst := corehosts.Instance{ID: "git.acme.dev", Kind: corehosts.KindGitLab, Base: "https://git.acme.dev", Token: true}
	data := corehosts.RepoState{
		MR:       &corehosts.MergeRequest{Number: 42, Title: "Add cart badge", State: corehosts.MRDraft, UpdatedAt: hostsAt},
		Pipeline: &corehosts.Pipeline{Status: corehosts.PipelineFailed, URL: "https://x/p", UpdatedAt: hostsAt},
	}
	links := corehosts.Links(inst, "acme-shop/shop-api", "feature/cart")
	until := hostsAt.Add(time.Minute)
	linked := func(ph corehosts.Phase) RepoHostView {
		return RepoHostView{Linked: true, Instance: inst, Project: "acme-shop/shop-api", Branch: "feature/cart", Links: links, Entry: corehosts.Entry{Phase: ph}}
	}
	f := &fakeHosts{views: map[core.RepoID]RepoHostView{
		"idle":     linked(corehosts.Idle{Data: data, FetchedAt: hostsAt}),
		"fetching": linked(corehosts.Fetching{Attempt: 1, Prev: corehosts.Idle{Data: data, FetchedAt: hostsAt}}),
		"retrying": linked(corehosts.Waiting{Until: until, Prev: corehosts.Idle{Data: data}, Why: corehosts.WaitTransient}),
		"blocked":  linked(corehosts.Blocked{Why: corehosts.BlockUnauthorized, Prev: corehosts.Idle{Data: data}}),
		"fresh":    linked(nil),
		"unlinked": {NotLinked: "This repo has no \"origin\" remote.", CredentialsInRemote: true},
	}}
	e := hostsEnv(t, f)
	get := func(id string) RepoHostResponse {
		resp, raw := e.do(t, http.MethodGet, "/api/repos/"+id+"/host", nil)
		var out RepoHostResponse
		if resp.StatusCode != 200 || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("GET %s = %d %s", id, resp.StatusCode, raw)
		}
		return out
	}
	for id, want := range map[string]HostPhase{"idle": HostPhaseIdle, "fetching": HostPhaseFetching, "retrying": HostPhaseRetrying, "blocked": HostPhaseBlocked, "fresh": HostPhaseIdle} {
		got := get(id)
		if got.Phase != want || !got.Linked || got.Project != "acme-shop/shop-api" || got.Kind != HostKindGitLab {
			t.Errorf("%s: %+v", id, got)
		}
		if id != "fresh" && (got.MR == nil || got.MR.State != "draft" || got.Pipeline == nil || got.Pipeline.Status != "failed") {
			t.Errorf("%s keeps no data: %+v", id, got)
		}
		if got.Links.MRList != links.MRList || got.Links.Branch != links.Branch {
			t.Errorf("%s links: %+v", id, got.Links)
		}
	}
	if r := get("retrying"); r.RetryAt == nil || !r.RetryAt.Equal(until) {
		t.Errorf("retryAt = %v", r.RetryAt)
	}
	if b := get("blocked"); b.BlockReason != "unauthorized" {
		t.Errorf("blockReason = %q", b.BlockReason)
	}
	if i := get("idle"); i.FetchedAt == nil || !i.FetchedAt.Equal(hostsAt) {
		t.Errorf("fetchedAt = %v", i.FetchedAt)
	}
	u := get("unlinked")
	if u.Linked || u.NotLinked == "" || !u.CredentialsInRemote || u.MR != nil {
		t.Errorf("unlinked = %+v", u)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/repos/nope/host", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo = %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/repos/a b/host", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("malformed repo = %d", resp.StatusCode)
	}
}

func TestGetMRsInbox(t *testing.T) {
	e := hostsEnv(t, &fakeHosts{})
	resp, data := e.do(t, http.MethodGet, "/api/mrs", nil)
	var body InboxResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &body) != nil {
		t.Fatalf("GET /api/mrs = %d %s", resp.StatusCode, data)
	}
	if len(body.Items) != 1 || body.Items[0].Role != "review_requested" || body.Items[0].Repo != "acme-shop/shop-web" || body.Items[0].Instance != "git.acme.dev" {
		t.Fatalf("items = %+v", body.Items)
	}
	if len(body.Instances) != 2 || body.Instances[0].Phase != HostPhaseIdle || body.Instances[1].Phase != HostPhaseRetrying || body.Instances[1].RetryAt == nil {
		t.Fatalf("instances = %+v", body.Instances)
	}
}

func TestParseRefresh(t *testing.T) {
	keys, err := ParseRefresh(RefreshHostsRequest{Repos: []core.RepoID{"rep_1"}, Instances: []string{"git.acme.dev"}})
	if err != nil || len(keys) != 2 || keys[0] != (corehosts.RepoKey{Repo: "rep_1"}) || keys[1] != (corehosts.InboxKey{Instance: "git.acme.dev"}) {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	if keys, err := ParseRefresh(RefreshHostsRequest{}); err != nil || len(keys) != 0 {
		t.Errorf("empty = %v, %v", keys, err)
	}
	for _, bad := range []RefreshHostsRequest{
		{Repos: []core.RepoID{"a b"}}, {Repos: []core.RepoID{""}},
		{Instances: []string{"Git.Acme.dev"}}, {Instances: []string{"a/b"}}, {Instances: []string{""}},
	} {
		if _, err := ParseRefresh(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestPostRefresh(t *testing.T) {
	f := &fakeHosts{}
	e := hostsEnv(t, f)
	resp, data := e.do(t, http.MethodPost, "/api/hosts/refresh", RefreshHostsRequest{Repos: []core.RepoID{"rep_1"}})
	if resp.StatusCode != http.StatusNoContent || len(f.refreshed) != 1 {
		t.Fatalf("refresh = %d %s (%v)", resp.StatusCode, data, f.refreshed)
	}
	if resp, _ := e.do(t, http.MethodPost, "/api/hosts/refresh", RefreshHostsRequest{Instances: []string{"BAD HOST"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad instance = %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, http.MethodPost, "/api/hosts/refresh", map[string]any{"nope": 1}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field = %d", resp.StatusCode)
	}
	f.refreshErr = apperr.New(apperr.Invalid, "That git host isn't configured.", nil)
	if resp, _ := e.do(t, http.MethodPost, "/api/hosts/refresh", RefreshHostsRequest{Instances: []string{"nowhere.dev"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown host = %d", resp.StatusCode)
	}
}

// Only GET reads and the one refresh POST exist: nothing writes to a host.
func TestHostRoutesAreReadOnly(t *testing.T) {
	e := hostsEnv(t, &fakeHosts{})
	for _, c := range [][2]string{
		{http.MethodPost, "/api/hosts"}, {http.MethodPut, "/api/hosts"}, {http.MethodDelete, "/api/hosts"},
		{http.MethodPost, "/api/mrs"}, {http.MethodPost, "/api/repos/rep_1/host"}, {http.MethodDelete, "/api/repos/rep_1/host"},
		{http.MethodPatch, "/api/mrs"},
	} {
		resp, _ := e.do(t, c[0], c[1], map[string]any{})
		if resp.StatusCode < 400 {
			t.Errorf("%s %s = %d", c[0], c[1], resp.StatusCode)
		}
	}
}

func TestPublishHostsChangedReachesSubscribers(t *testing.T) {
	e := hostsEnv(t, &fakeHosts{})
	ws := e.dial(t)
	subscribe(t, ws, "hosts")
	time.Sleep(150 * time.Millisecond)
	id := core.RepoID("rep_1")
	e.srv.PublishHostsChanged(HostsChangedMessage{Repo: &id})
	m, err := readMsg(t, ws, 5*time.Second)
	if err != nil || m.Type != MsgEvent || m.Topic != "hosts" {
		t.Fatalf("message = %+v, %v", m, err)
	}
	var got HostsChangedMessage
	raw, _ := json.Marshal(m.Data)
	if json.Unmarshal(raw, &got) != nil || got.Repo == nil || *got.Repo != id {
		t.Errorf("data = %s", raw)
	}
}
