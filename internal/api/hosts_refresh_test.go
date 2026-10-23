package api

import (
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// formatRefresh is the inverse of ParseRefresh.
func formatRefresh(keys []corehosts.Key) RefreshHostsRequest {
	var req RefreshHostsRequest
	for _, k := range keys {
		switch v := k.(type) {
		case corehosts.RepoKey:
			req.Repos = append(req.Repos, v.Repo)
		case corehosts.InboxKey:
			req.Instances = append(req.Instances, string(v.Instance))
		}
	}
	return req
}

func TestParseRefreshRoundTrip(t *testing.T) {
	req := RefreshHostsRequest{Repos: []core.RepoID{"rep_1", "rep_2"}, Instances: []string{"git.acme.dev", "github.com"}}
	keys, err := ParseRefresh(req)
	if err != nil {
		t.Fatal(err)
	}
	back := formatRefresh(keys)
	if strings.Join(idStrings(back.Repos), ",") != "rep_1,rep_2" || strings.Join(back.Instances, ",") != "git.acme.dev,github.com" {
		t.Fatalf("round trip = %+v", back)
	}
	again, err := ParseRefresh(back)
	if err != nil || len(again) != len(keys) {
		t.Fatalf("second parse = %v, %v", again, err)
	}
	for i := range keys {
		if keys[i] != again[i] {
			t.Fatalf("key %d: %v != %v", i, keys[i], again[i])
		}
	}
}

func idStrings(ids []core.RepoID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

func FuzzParseRefresh(f *testing.F) {
	f.Add("rep_1,rep_2", "git.acme.dev")
	f.Add("", "")
	f.Add("a b", "Git.Acme.dev")
	f.Fuzz(func(t *testing.T, repos, insts string) {
		var req RefreshHostsRequest
		if repos != "" {
			for _, r := range strings.Split(repos, ",") {
				req.Repos = append(req.Repos, core.RepoID(r))
			}
		}
		if insts != "" {
			req.Instances = strings.Split(insts, ",")
		}
		keys, err := ParseRefresh(req)
		if err != nil {
			return
		}
		// Accepted implies valid, and nothing is invented or lost.
		if len(keys) != len(req.Repos)+len(req.Instances) {
			t.Fatalf("%d keys for %+v", len(keys), req)
		}
		for _, k := range keys {
			switch v := k.(type) {
			case corehosts.RepoKey:
				if v.Repo.Validate() != nil || v.Branch != "" {
					t.Fatalf("bad repo key %+v", v)
				}
			case corehosts.InboxKey:
				if v.Instance.Validate() != nil {
					t.Fatalf("bad inbox key %+v", v)
				}
			default:
				t.Fatalf("unknown key %T", k)
			}
		}
		back := formatRefresh(keys)
		again, err := ParseRefresh(back)
		if err != nil || len(again) != len(keys) {
			t.Fatalf("round trip: %v, %v", again, err)
		}
		for i := range keys {
			if keys[i] != again[i] {
				t.Fatalf("round trip key %d: %v != %v", i, keys[i], again[i])
			}
		}
	})
}
