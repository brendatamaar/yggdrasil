package hosts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// reply is one recorded host answer.
type reply struct {
	status int
	body   string
	header map[string]string
}

// A real empty GitHub repository answers git-refs calls with 409 "Git
// Repository is empty" (recorded shape; not verified against a live host).
var emptyRepo409 = reply{status: 409, body: `{"message":"Git Repository is empty.","documentation_url":"https://docs.github.com/rest/git/refs"}`}

func lookupFixture(t *testing.T, replies map[string]reply) (*ConfirmedHosts, CreateRepoSpec, func() []string) {
	t.Helper()
	description := "Created by Yggdrasil [ygg:" + strings.Repeat("a", 16) + "]"
	var mu sync.Mutex
	var seen []string
	srv, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("lookup must be read-only, got %s", r.Method)
		}
		route := r.URL.Path
		switch {
		case strings.HasSuffix(route, "/git/matching-refs/"):
			route = "refs"
		case strings.HasSuffix(route, "/branches"):
			route = "branches"
		case strings.HasSuffix(route, "/tags"):
			route = "tags"
		case route == "/api/v3/repos/acme/shop":
			fmt.Fprintf(w, `{"id":42,"name":"shop","owner":{"login":"acme"},"private":true,"description":%q,"clone_url":%q}`, description, "https://"+r.Host+"/acme/shop.git")
			return
		}
		rep, ok := replies[route]
		if !ok {
			t.Errorf("unexpected request %s", r.URL.String())
			w.WriteHeader(500)
			return
		}
		for k, v := range rep.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rep.status)
		fmt.Fprint(w, rep.body)
	})
	h := NewConfirmedHosts(New(Deps{Config: cfg}), nil, srv.Client())
	spec := CreateRepoSpec{Instance: in.ID, Owner: "acme", OwnerKind: "user", Name: "shop", Description: description, Private: true}
	return h, spec, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func okReply(body string) reply { return reply{status: 200, body: body} }

func TestLookupRepoEmptyRefProof(t *testing.T) {
	cases := []struct {
		name    string
		replies map[string]reply
		want    RepoLookupOutcome
		wantErr bool
		probes  int // requests after the repository read
	}{
		{"matching-refs 200 [] is empty", map[string]reply{"refs": okReply("[]")}, RepoExactMatch, false, 1},
		{"matching-refs lists a ref", map[string]reply{"refs": okReply(`[{"ref":"refs/heads/main"}]`)}, RepoConflict, false, 1},
		{"409 then branches and tags empty", map[string]reply{"refs": emptyRepo409, "branches": okReply("[]"), "tags": okReply("[]")}, RepoExactMatch, false, 3},
		{"409 then branches not empty", map[string]reply{"refs": emptyRepo409, "branches": okReply(`[{"name":"main"}]`)}, RepoConflict, false, 2},
		{"409 then branches empty and tags not empty", map[string]reply{"refs": emptyRepo409, "branches": okReply("[]"), "tags": okReply(`[{"name":"v1"}]`)}, RepoConflict, false, 3},
		{"409 then branches error", map[string]reply{"refs": emptyRepo409, "branches": {status: 500}}, RepoUnknown, true, 2},
		{"409 then branches 404 is not proof", map[string]reply{"refs": emptyRepo409, "branches": {status: 404}}, RepoUnknown, true, 2},
		{"409 then tags error", map[string]reply{"refs": emptyRepo409, "branches": okReply("[]"), "tags": {status: 500}}, RepoUnknown, true, 3},
		{"409 then branches not a list", map[string]reply{"refs": emptyRepo409, "branches": okReply(`{"message":"x"}`)}, RepoUnknown, false, 2},
		{"409 then branches continue on another page", map[string]reply{"refs": emptyRepo409, "branches": {status: 200, body: "[]", header: map[string]string{"Link": `<https://x/y>; rel="next"`}}}, RepoUnknown, false, 2},
		{"matching-refs 404 never proves empty", map[string]reply{"refs": {status: 404}}, RepoUnknown, true, 1},
		{"matching-refs 500", map[string]reply{"refs": {status: 500}}, RepoUnknown, true, 1},
		{"matching-refs 403", map[string]reply{"refs": {status: 403}}, RepoUnknown, true, 1},
		{"matching-refs not a list", map[string]reply{"refs": okReply(`{"message":"x"}`)}, RepoUnknown, false, 1},
		{"matching-refs null", map[string]reply{"refs": okReply(`null`)}, RepoUnknown, false, 1},
		{"matching-refs continues", map[string]reply{"refs": {status: 200, body: "[]", header: map[string]string{"X-Next-Page": "2"}}}, RepoUnknown, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, spec, seen := lookupFixture(t, c.replies)
			got, err := h.LookupRepo(context.Background(), spec)
			if got.Outcome != c.want || (err != nil) != c.wantErr {
				t.Fatalf("outcome %q err %v, want %q (err %v)", got.Outcome, err, c.want, c.wantErr)
			}
			if got.Outcome == RepoExactMatch && (got.RemoteID != "42" || got.URL == "") {
				t.Fatal("exact match without the remote identity", got)
			}
			if n := len(seen()) - 1; n != c.probes {
				t.Fatalf("%d ref probes, want %d: %v", n, c.probes, seen())
			}
		})
	}
}

// The 409 marker must not leak into other statuses: only a real 409 falls back.
func TestLookupRepoFallbackOnlyOn409(t *testing.T) {
	h, spec, seen := lookupFixture(t, map[string]reply{"refs": {status: 422}})
	if got, err := h.LookupRepo(context.Background(), spec); got.Outcome != RepoUnknown || err == nil || errors.Is(err, errStatusConflict) {
		t.Fatal(got, err)
	}
	if len(seen()) != 2 {
		t.Fatal("fell back after a non-409", seen())
	}
}
