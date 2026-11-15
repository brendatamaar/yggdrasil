package hosts

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

func TestContextImmutableRunJobsAndHead(t *testing.T) {
	for _, lab := range []bool{false, true} {
		t.Run(fmt.Sprint(lab), func(t *testing.T) {
			head := strings.Repeat("a", 40)
			srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/trace") || strings.Contains(r.URL.Path, "/logs"):
					fmt.Fprint(w, "failed: sentinel-secret\n")
				case strings.HasSuffix(r.URL.Path, "/jobs"):
					if lab {
						fmt.Fprint(w, `[{"id":19,"name":"test","status":"failed"}]`)
					} else {
						fmt.Fprint(w, `{"jobs":[{"id":19,"name":"test","conclusion":"failure"}]}`)
					}
				default:
					fmt.Fprintf(w, `{"id":12,"sha":%q,"head_sha":%q}`, head, head)
				}
			})
			api := ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL, GitLab: lab}
			result, err := api.FailedJobs(context.Background(), in, "sentinel-secret", "acme/shop", "12", head)
			if err != nil {
				t.Fatal(err)
			}
			jobs := result.Value.(corehosts.JobLogs)
			if jobs.RunID != "12" || jobs.Head != head || len(jobs.Jobs) != 1 || jobs.Jobs[0].JobID != "19" || strings.Contains(jobs.Jobs[0].Log, "sentinel-secret") {
				t.Fatal(jobs)
			}
			if _, err := api.FailedJobs(context.Background(), in, "sentinel-secret", "acme/shop", "12", strings.Repeat("b", 40)); err == nil {
				t.Fatal("foreign run head accepted")
			}
		})
	}
}
func TestContextImmutableMRDiff(t *testing.T) {
	head := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	var moved atomic.Bool
	srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/compare/") {
			fmt.Fprint(w, `{"files":[{"filename":"main.go","patch":"+sentinel-secret"}]}`)
			return
		}
		current := head
		if moved.Load() {
			current = base
		}
		fmt.Fprintf(w, `{"number":3,"title":"Review","state":"open","head":{"sha":%q},"base":{"sha":%q}}`, current, base)
	})
	api := ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL}
	result, err := api.MRDiff(context.Background(), in, "sentinel-secret", "acme/shop", 3)
	if err != nil {
		t.Fatal(err)
	}
	value := result.Value.(corehosts.MRContext)
	if value.Head != head || !strings.Contains(value.Diff, "main.go") || strings.Contains(value.Diff, "sentinel-secret") {
		t.Fatal(value)
	}
	moved.Store(true)
	result, err = api.MRDiff(context.Background(), in, "sentinel-secret", "acme/shop", 3)
	if err != nil || result.Value.(corehosts.MRContext).Head != base {
		t.Fatal("stale head returned", result, err)
	}
}
func TestConfirmedRepoExactReconciliation(t *testing.T) {
	description := "Created by Yggdrasil [ygg:" + strings.Repeat("a", 16) + "]"
	var conflict atomic.Bool
	srv, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/matching-refs/") {
			fmt.Fprint(w, "[]")
			return
		}
		owner := "acme"
		if conflict.Load() {
			owner = "other"
		}
		fmt.Fprintf(w, `{"id":42,"name":"shop","owner":{"login":%q},"private":true,"description":%q,"clone_url":%q}`, owner, description, "https://"+r.Host+"/acme/shop.git")
	})
	h := NewConfirmedHosts(New(Deps{Config: cfg}), nil, srv.Client())
	spec := CreateRepoSpec{Instance: in.ID, Owner: "acme", OwnerKind: "org", Name: "shop", Description: description, Private: true}
	lookup, err := h.LookupRepo(context.Background(), spec)
	if err != nil || lookup.Outcome != RepoExactMatch || lookup.RemoteID != "42" {
		t.Fatal(lookup, err)
	}
	conflict.Store(true)
	lookup, err = h.LookupRepo(context.Background(), spec)
	if err != nil || lookup.Outcome != RepoConflict {
		t.Fatal("unrelated remote adopted", lookup, err)
	}
}

func TestContextPaginationUsesExplicitContinuation(t *testing.T) {
	var calls atomic.Int32
	srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<https://untrusted.invalid/ignored>; rel="next"`)
			fmt.Fprint(w, `[{"number":1,"title":"first"}]`)
			return
		}
		fmt.Fprint(w, `[{"number":2,"title":"second"}]`)
	})
	api := ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL}
	result, err := api.Issues(context.Background(), in, "sentinel-secret", "acme/shop")
	if err != nil || calls.Load() != 2 || len(result.Value.(corehosts.IssueList).Items) != 2 {
		t.Fatal("short page lost its continuation", result, err, calls.Load())
	}
}

func TestContextPublicGitHubIssueDisplayOrigin(t *testing.T) {
	api := ContextAPI{Root: "https://api.github.com"}
	for _, link := range []string{"https://github.com/acme/shop/issues/1", "https://foreign.acme.test/acme/shop/issues/1", "https://user:password@github.com/acme/shop/issues/1"} {
		value, err := api.issue(contextIssueWire{Number: 1, Title: "Issue", HTMLURL: link})
		if err != nil {
			t.Fatal(err)
		}
		if link == "https://github.com/acme/shop/issues/1" {
			if value.URL != link {
				t.Fatal("lost configured GitHub web link")
			}
		} else if value.URL != "" {
			t.Fatal("unsafe display origin accepted")
		}
	}
}

func TestHostContextAggregateBudgetChargesFailedReads(t *testing.T) {
	head := strings.Repeat("a", 40)
	var logs atomic.Int32
	srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/trace"):
			logs.Add(1)
			fmt.Fprint(w, strings.Repeat("x", corehosts.ContextMaxResponseBytes+1))
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			fmt.Fprint(w, `[{"id":1,"status":"failed"},{"id":2,"status":"failed"}]`)
		default:
			fmt.Fprintf(w, `{"id":12,"sha":%q}`, head)
		}
	})
	api := ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL, GitLab: true}
	result, err := api.FailedJobs(context.Background(), in, "sentinel-secret", "acme/shop", "12", head)
	if err != nil {
		t.Fatal(err)
	}
	if logs.Load() != 1 {
		t.Fatalf("aggregate budget continued after oversized log: %d requests", logs.Load())
	}
	if !result.Value.(corehosts.JobLogs).Truncated || len(result.Unavailable) == 0 {
		t.Fatal("missing partial budget outcome")
	}
}
