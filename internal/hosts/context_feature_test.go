package hosts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

type contextResolverFunc func(context.Context, corehosts.ContextKey) (ContextTarget, error)

func (f contextResolverFunc) ResolveContext(ctx context.Context, k corehosts.ContextKey) (ContextTarget, error) {
	return f(ctx, k)
}

type contextClientFake struct {
	HostClient
	reads atomic.Int32
	gate  chan struct{}
}

func (f *contextClientFake) Issues(ctx context.Context, in corehosts.Instance, token string, p corehosts.ProjectPath) (ContextResult, error) {
	f.reads.Add(1)
	if f.gate != nil {
		select {
		case <-ctx.Done():
			return ContextResult{}, ctx.Err()
		case <-f.gate:
		}
	}
	return ContextResult{Value: corehosts.IssueList{Items: []corehosts.IssueSummary{{Number: 1, Title: "hello " + token}}}}, nil
}
func (f *contextClientFake) Issue(context.Context, corehosts.Instance, string, corehosts.ProjectPath, int) (ContextResult, error) {
	return ContextResult{}, ErrContextUnsupported
}
func (f *contextClientFake) MRDiff(context.Context, corehosts.Instance, string, corehosts.ProjectPath, int) (ContextResult, error) {
	return ContextResult{}, ErrContextUnsupported
}
func (f *contextClientFake) FailedJobs(context.Context, corehosts.Instance, string, corehosts.ProjectPath, string, string) (ContextResult, error) {
	return ContextResult{}, ErrContextUnsupported
}

func contextFixture(t *testing.T, handler http.HandlerFunc) (*httptest.Server, corehosts.Instance, Config) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	cfg := ParseConfig(map[string]Var{"YGG_HOST_1": {Value: "github " + srv.URL}, "YGG_HOST_1_TOKEN": {Value: "sentinel-secret"}})
	var inst corehosts.Instance
	for _, in := range cfg.CoreInstances() {
		if in.Base == srv.URL {
			inst = in
		}
	}
	return srv, inst, cfg
}
func TestContextScopeCacheRedactionAndCancellation(t *testing.T) {
	_, inst, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") })
	fake := &contextClientFake{}
	var tracked atomic.Bool
	tracked.Store(true)
	resolve := contextResolverFunc(func(ctx context.Context, k corehosts.ContextKey) (ContextTarget, error) {
		if !tracked.Load() {
			return ContextTarget{}, ErrContextScope
		}
		return ContextTarget{Instance: inst.ID, Project: "acme/shop", Revision: "v1"}, nil
	})
	s := New(Deps{Config: cfg, Clients: map[corehosts.Kind]HostClient{corehosts.KindGitHub: fake}})
	c := NewContextService(s, resolve)
	key := corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssues}
	first, err := c.Read(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(first.Result.Value), "sentinel-secret") {
		t.Fatal("credential escaped")
	}
	first.Result.Value.(corehosts.IssueList).Items[0].Title = "changed"
	cached, err := c.Read(context.Background(), key)
	if err != nil || fake.reads.Load() != 1 || cached.Result.Value.(corehosts.IssueList).Items[0].Title == "changed" {
		t.Fatal("cache alias or missing cache", err)
	}
	tracked.Store(false)
	if _, err = c.Read(context.Background(), key); !errors.Is(err, ErrContextScope) {
		t.Fatal("untracked cache exposed", err)
	}
	tracked.Store(true)
	fake.gate = make(chan struct{})
	key.Repo = "rep_2"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Read(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel ignored", err)
	}
}
func TestContextRevalidatesAfterRead(t *testing.T) {
	_, inst, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	fake := &contextClientFake{}
	var calls int
	resolve := contextResolverFunc(func(ctx context.Context, k corehosts.ContextKey) (ContextTarget, error) {
		calls++
		if calls >= 3 {
			return ContextTarget{}, ErrContextScope
		}
		return ContextTarget{Instance: inst.ID, Project: "acme/shop", Revision: "v1"}, nil
	})
	c := NewContextService(New(Deps{Config: cfg, Clients: map[corehosts.Kind]HostClient{corehosts.KindGitHub: fake}}), resolve)
	if _, err := c.Read(context.Background(), corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssues}); !errors.Is(err, ErrContextScope) {
		t.Fatal("scope change accepted", err)
	}
}

// Same capability clauses exercise a fake and the bounded HTTP adapter.
func contextIssuesContract(t *testing.T, client ContextClient, inst corehosts.Instance) {
	t.Helper()
	for i := 0; i < 2; i++ {
		r, err := client.Issues(context.Background(), inst, "sentinel-secret", "acme/shop")
		if err != nil {
			t.Fatal(err)
		}
		list, ok := r.Value.(corehosts.IssueList)
		if !ok || len(list.Items) != 1 || list.Items[0].Number != 1 {
			t.Fatal(r)
		}
	}
}
func TestContextIssuesCapabilityContract(t *testing.T) {
	t.Run("fake", func(t *testing.T) { contextIssuesContract(t, &contextClientFake{}, corehosts.Instance{}) })
	for _, lab := range []bool{false, true} {
		t.Run(fmt.Sprint("http-gitlab-", lab), func(t *testing.T) {
			srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
				header := r.Header.Get("Authorization")
				if lab {
					header = r.Header.Get("PRIVATE-TOKEN")
				}
				if !strings.Contains(header, "sentinel-secret") {
					t.Error("missing token header")
				}
				if r.Method != "GET" {
					t.Error("read attempted mutation")
				}
				if lab {
					fmt.Fprint(w, `[{"iid":1,"title":"hello sentinel-secret","author":{"username":"alice"},"labels":["bug"]}]`)
				} else {
					fmt.Fprint(w, `[{"number":1,"title":"hello sentinel-secret","user":{"login":"alice"},"labels":[{"name":"bug"}]}]`)
				}
			})
			api := ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL, GitLab: lab}
			contextIssuesContract(t, api, in)
			result, err := api.Issues(context.Background(), in, "sentinel-secret", "acme/shop")
			if err != nil || strings.Contains(fmt.Sprint(result), "sentinel-secret") {
				t.Fatal("unredacted context", result, err)
			}
		})
	}
}
func TestContextBoundaryAndHTTPFailures(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			_, err := (ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL}).Issues(context.Background(), in, "sentinel-secret", "acme/shop")
			var fe *FetchError
			if !errors.As(err, &fe) {
				t.Fatal("unclassified failure", err)
			}
			want := corehosts.FailTransient
			switch status {
			case 401, 403:
				want = corehosts.FailUnauthorized
			case 404:
				want = corehosts.FailNotFound
			case 429:
				want = corehosts.FailRateLimited
			}
			if fe.Kind != want {
				t.Fatal(fe.Kind, want)
			}
		})
	}
	srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", corehosts.ContextMaxResponseBytes+1))
	})
	api := ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL}
	if _, err := api.Issues(context.Background(), in, "sentinel-secret", "acme/shop"); !errors.Is(err, ErrContextTooLarge) {
		t.Fatal("oversized body", err)
	}
	for _, p := range []corehosts.ProjectPath{"", "a/../b", "a/b?secret", "a/b%2f..", "a/b\\c"} {
		if _, err := api.Issues(context.Background(), in, "sentinel-secret", p); !errors.Is(err, ErrContextScope) {
			t.Fatal("invalid project", p, err)
		}
	}
}
func TestContextRedirectDropsCredentials(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer other.Close()
	srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
	_, err := (ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL}).Issues(context.Background(), in, "sentinel-secret", "acme/shop")
	if err == nil || leaked.Load() != 0 {
		t.Fatal("redirect escaped origin")
	}
}
func TestConfirmedCommentSinglePostAndExactReconciliation(t *testing.T) {
	head := strings.Repeat("a", 40)
	marker := strings.Repeat("b", 16)
	body := "Review text"
	var posts atomic.Int32
	stored := ""
	var stateMu sync.Mutex
	srv, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		stateMu.Lock()
		defer stateMu.Unlock()
		if strings.Contains(r.URL.Path, "/pulls/") {
			fmt.Fprintf(w, `{"number":7,"head":{"sha":%q}}`, head)
			return
		}
		if r.Method == "POST" {
			posts.Add(1)
			var data struct {
				Body string `json:"body"`
			}
			if json.NewDecoder(r.Body).Decode(&data) != nil {
				t.Error("bad body")
			}
			stored = data.Body
			w.WriteHeader(503)
			return
		}
		fmt.Fprintf(w, `[{"id":99,"body":%q}]`, stored)
	})
	resolve := contextResolverFunc(func(context.Context, corehosts.ContextKey) (ContextTarget, error) {
		return ContextTarget{Instance: in.ID, Project: "acme/shop", Revision: "1"}, nil
	})
	h := NewConfirmedHosts(New(Deps{Config: cfg}), resolve, srv.Client())
	_, err := h.PostReviewComment(context.Background(), "rep_1", 7, head, body, marker)
	if !errors.Is(err, ErrOutcomeUnknown) || posts.Load() != 1 {
		t.Fatal("post retried or unknown hidden", posts.Load(), err)
	}
	digest := sha256.Sum256([]byte(body))
	lookup, err := h.FindReviewComment(context.Background(), "rep_1", 7, marker, hex.EncodeToString(digest[:]), head)
	if err != nil || lookup.Outcome != coreproposals.CommentFound || lookup.CommentID != "99" || posts.Load() != 1 {
		t.Fatal(lookup, err, posts.Load())
	}
	stateMu.Lock()
	stored += "changed"
	stateMu.Unlock()
	lookup, err = h.FindReviewComment(context.Background(), "rep_1", 7, marker, hex.EncodeToString(digest[:]), head)
	if err != nil || lookup.Outcome != coreproposals.CommentConflict {
		t.Fatal("adopted different body", lookup, err)
	}
}
func TestContextGetterConcurrencyGate(t *testing.T) {
	var active, max atomic.Int32
	srv, _, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		for {
			m := max.Load()
			if n <= m || max.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(time.Millisecond * 3)
		active.Add(-1)
		fmt.Fprint(w, "[]")
	})
	g := NewGetter(srv.Client(), nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.GetBounded(context.Background(), srv.URL, "", AuthBearer, 100)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if max.Load() > 4 {
		t.Fatal("more than four concurrent requests", max.Load())
	}
}
func FuzzContextIssueBoundary(f *testing.F) {
	f.Add(`{"number":1,"title":"hello","labels":[{"name":"bug"}]}`)
	f.Fuzz(func(t *testing.T, raw string) {
		var wire contextIssueWire
		if json.Unmarshal([]byte(raw), &wire) != nil {
			return
		}
		d, err := (ContextAPI{Root: "https://git.acme.test"}).issue(wire)
		if err == nil {
			if d.Summary.Number <= 0 || d.Summary.Title == "" {
				t.Fatal("invalid accepted")
			}
			again, err := (ContextAPI{Root: "https://git.acme.test"}).issue(wire)
			if err != nil || !reflect.DeepEqual(d, again) {
				t.Fatal("unstable parse")
			}
		}
	})
}
