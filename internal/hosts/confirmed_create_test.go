package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConfirmedCreateSinglePostAndLostReplyLookup(t *testing.T) {
	description := "Created by Yggdrasil [ygg:" + strings.Repeat("a", 16) + "]"
	var posts atomic.Int32
	var created atomic.Bool
	srv, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sentinel-secret" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/api/v3/users/acme":
			fmt.Fprint(w, `{"login":"acme","type":"User"}`)
		case "/api/v3/user":
			fmt.Fprint(w, `{"login":"acme"}`)
		case "/api/v3/user/repos":
			if r.Method != "POST" {
				t.Error("expected post")
			}
			posts.Add(1)
			var body struct {
				Private     bool   `json:"private"`
				AutoInit    bool   `json:"auto_init"`
				Description string `json:"description"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || !body.Private || body.AutoInit || body.Description != description {
				t.Error("creation widened input", body)
			}
			created.Store(true)
			w.WriteHeader(503)
		default:
			if strings.Contains(r.URL.Path, "/git/matching-refs/") {
				fmt.Fprint(w, "[]")
				return
			}
			if !created.Load() {
				w.WriteHeader(404)
				return
			}
			fmt.Fprintf(w, `{"id":42,"name":"shop","owner":{"login":"acme"},"private":true,"description":%q,"clone_url":%q}`, description, "https://"+r.Host+"/acme/shop.git")
		}
	})
	h := NewConfirmedHosts(New(Deps{Config: cfg}), nil, srv.Client())
	spec := CreateRepoSpec{Instance: in.ID, Owner: "acme", OwnerKind: "user", Name: "shop", Description: description, Private: true}
	if _, err := h.CreateRepo(context.Background(), spec); !errors.Is(err, ErrOutcomeUnknown) || posts.Load() != 1 {
		t.Fatal("create retried or lost outcome hidden", err, posts.Load())
	}
	lookup, err := h.LookupRepo(context.Background(), spec)
	if err != nil || lookup.Outcome != RepoExactMatch || posts.Load() != 1 {
		t.Fatal(lookup, err, posts.Load())
	}
}
