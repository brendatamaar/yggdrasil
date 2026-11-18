package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

// fakeMRHost is the cached host link of one repo and the bounded reader behind
// it. It records every key it is asked to read.
type fakeMRHost struct {
	repo core.RepoID
	mr   *corehosts.MergeRequest
	out  hosts.ContextOutcome

	mu   sync.Mutex
	keys []corehosts.ContextKey
}

func (f *fakeMRHost) RepoHost(_ context.Context, repo core.RepoID) (api.RepoHostView, error) {
	if repo != f.repo {
		return api.RepoHostView{NotLinked: "no host"}, nil
	}
	return api.RepoHostView{
		Linked: true, Instance: corehosts.Instance{ID: "git.acme.dev", Kind: corehosts.KindGitLab, Token: true}, Project: "acme/shop-api",
		Entry: corehosts.Entry{Phase: corehosts.Fetching{Attempt: 2, Prev: corehosts.Idle{Data: corehosts.RepoState{MR: f.mr}}}},
	}, nil
}

func (f *fakeMRHost) Read(_ context.Context, key corehosts.ContextKey) (hosts.ContextOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	return f.out, nil
}

func (f *fakeMRHost) reads() []corehosts.ContextKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]corehosts.ContextKey(nil), f.keys...)
}

func (r *p6Rig) bindMRHost(p *p6Repo, f *fakeMRHost) {
	r.a.srv.SetPhase6Services(api.Phase6Services{HostContext: hostContextAPI{
		reader: f, repoHost: f,
		tracked: func(_ context.Context, id core.RepoID) error {
			if id != p.id {
				return db.ErrNotFound
			}
			return nil
		},
	}})
}

// The merge request context is served through the route for the head the cache
// holds: pinned, bounded and redacted by the reader, with the same statuses as
// every other host read. A merge request with no head yet is a retryable wait;
// one the cache does not hold is not found; none of these reads a head the
// browser chose.
func TestPhase6MRContextIsServedForTheCachedHead(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	head := strings.Repeat("c", 40)
	path := "/api/repos/" + string(p.id) + "/mrs/3/context"
	open := func(h string) *corehosts.MergeRequest {
		return &corehosts.MergeRequest{Number: 3, Title: "Fix cart total", State: corehosts.MROpen, Source: "fix-cart", Target: "main", Head: h}
	}

	t.Run("ready for the cached head", func(t *testing.T) {
		f := &fakeMRHost{repo: p.id, mr: open(head), out: hosts.ContextOutcome{Result: &hosts.ContextResult{Value: corehosts.MRContext{
			MR: *open(head), Head: head, Diff: "diff --git a/cart.go b/cart.go\n",
		}}}}
		r.bindMRHost(p, f)
		var got api.MRContextResponse
		if code := r.get(t, path+"?head="+strings.Repeat("d", 40), &got); code != 200 {
			t.Fatalf("GET = %d", code)
		}
		if got.Status.Phase != api.HostContextReady || got.Head != head || !strings.Contains(got.Diff, "cart.go") || got.MR == nil || got.MR.Title != "Fix cart total" {
			t.Fatalf("response = %+v", got)
		}
		keys := f.reads()
		if len(keys) != 1 || keys[0].Kind != corehosts.ContextMRDiff || keys[0].ObjectID != "3" || keys[0].Head != head || keys[0].Repo != p.id {
			t.Fatalf("keys read = %+v; want one mr_diff read pinned to the cached head, not the query's", keys)
		}
	})

	t.Run("an oversized diff is cut and labelled", func(t *testing.T) {
		f := &fakeMRHost{repo: p.id, mr: open(head), out: hosts.ContextOutcome{Result: &hosts.ContextResult{Value: corehosts.MRContext{
			MR: *open(head), Head: head, Diff: strings.Repeat("+x\n", 100<<10),
		}}}}
		r.bindMRHost(p, f)
		var got api.MRContextResponse
		if code := r.get(t, path, &got); code != 200 || !got.DiffTruncated || len(got.Diff) > 64<<10 {
			t.Fatalf("GET = %d, truncated %v, %d bytes", code, got.DiffTruncated, len(got.Diff))
		}
	})

	t.Run("no head yet is a retryable wait and reads nothing", func(t *testing.T) {
		f := &fakeMRHost{repo: p.id, mr: open("")}
		r.bindMRHost(p, f)
		var got api.MRContextResponse
		if code := r.get(t, path, &got); code != 200 {
			t.Fatalf("GET = %d", code)
		}
		if got.Status.Phase != api.HostContextWaiting || got.Status.Reason == api.HostContextUnsupported || got.Status.Message == "" {
			t.Fatalf("status = %+v; want waiting, not unsupported", got.Status)
		}
		if len(f.reads()) != 0 {
			t.Fatalf("a read was made without a head: %+v", f.reads())
		}
	})

	t.Run("a merge request the cache does not hold is not found", func(t *testing.T) {
		f := &fakeMRHost{repo: p.id, mr: open(head)}
		r.bindMRHost(p, f)
		var got api.MRContextResponse
		if code := r.get(t, "/api/repos/"+string(p.id)+"/mrs/4/context", &got); code != 200 {
			t.Fatalf("GET = %d", code)
		}
		if got.Status.Phase != api.HostContextBlocked || got.Status.Reason != api.HostContextNotFound || len(f.reads()) != 0 {
			t.Fatalf("status = %+v, reads %+v", got.Status, f.reads())
		}
	})

	t.Run("a blocked read keeps its reason", func(t *testing.T) {
		f := &fakeMRHost{repo: p.id, mr: open(head), out: hosts.ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: corehosts.ContextNoToken}}}
		r.bindMRHost(p, f)
		var got api.MRContextResponse
		if code := r.get(t, path, &got); code != 200 || got.Status.Phase != api.HostContextBlocked || got.Status.Reason != api.HostContextNoToken || got.Diff != "" {
			t.Fatalf("GET = %d %+v", code, got)
		}
	})

	t.Run("an unknown repo is 404", func(t *testing.T) {
		f := &fakeMRHost{repo: p.id, mr: open(head)}
		r.bindMRHost(p, f)
		if code := r.get(t, "/api/repos/rep_0000000000000000/mrs/3/context", nil); code != 404 {
			t.Fatalf("GET = %d, want 404", code)
		}
		if len(f.reads()) != 0 {
			t.Fatal("a read was made for an untracked repo")
		}
	})
}
