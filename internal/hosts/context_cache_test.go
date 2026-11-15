package hosts

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

func TestContextCredentialAndTTLCacheFences(t *testing.T) {
	_, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	fake := &contextClientFake{}
	now := time.Unix(100, 0)
	resolve := contextResolverFunc(func(context.Context, corehosts.ContextKey) (ContextTarget, error) {
		return ContextTarget{Instance: in.ID, Project: "acme/shop", Revision: "1"}, nil
	})
	s := New(Deps{Config: cfg, Clock: func() time.Time { return now }, Clients: map[corehosts.Kind]HostClient{corehosts.KindGitHub: fake}})
	c := NewContextService(s, resolve)
	key := corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssues}
	for _, seconds := range []int{0, 1, 60} {
		now = time.Unix(int64(100+seconds), 0)
		if _, err := c.Read(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
	if fake.reads.Load() != 2 {
		t.Fatal("cache TTL ignored", fake.reads.Load())
	}
	s.mu.Lock()
	s.cfg.tokens[in.ID] = "changed-secret"
	s.mu.Unlock()
	if _, err := c.Read(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if fake.reads.Load() != 3 {
		t.Fatal("credential changed without invalidation")
	}
	s.mu.Lock()
	delete(s.cfg.tokens, in.ID)
	s.mu.Unlock()
	out, err := c.Read(context.Background(), key)
	if err != nil || out.Blocked == nil || out.Blocked.Reason != corehosts.ContextNoToken {
		t.Fatal("missing token cache exposed", out, err)
	}
}
func TestContextConcurrentReadersJoinAndDetach(t *testing.T) {
	_, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	fake := &contextClientFake{gate: make(chan struct{})}
	resolve := contextResolverFunc(func(context.Context, corehosts.ContextKey) (ContextTarget, error) {
		return ContextTarget{Instance: in.ID, Project: "acme/shop", Revision: "1"}, nil
	})
	s := New(Deps{Config: cfg, Clients: map[corehosts.Kind]HostClient{corehosts.KindGitHub: fake}})
	c := NewContextService(s, resolve)
	key := corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssues}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := c.Read(context.Background(), key); err != nil {
			t.Error(err)
		}
	}()
	// Synchronize through the client read counter rather than hoping both requests
	// overlap. The bound keeps a broken interpreter from hanging this test.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for fake.reads.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("read did not start")
		case <-tick.C:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Read(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatal("joining caller failed to detach", err)
	}
	close(fake.gate)
	wg.Wait()
	if _, err := c.Read(context.Background(), key); err != nil || fake.reads.Load() != 1 {
		t.Fatal("joined read duplicated", err)
	}
}

type rateContextClient struct {
	*contextClientFake
	until time.Time
}

func (f rateContextClient) Issues(context.Context, corehosts.Instance, string, corehosts.ProjectPath) (ContextResult, error) {
	f.reads.Add(1)
	return ContextResult{}, &FetchError{Kind: corehosts.FailRateLimited, RetryAt: &f.until, Err: errors.New("rate limited")}
}
func TestContextRateLimitReturnsDeadlineWithoutDetachedWork(t *testing.T) {
	_, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected network") })
	client := rateContextClient{contextClientFake: &contextClientFake{}, until: time.Now().Add(time.Minute)}
	resolve := contextResolverFunc(func(context.Context, corehosts.ContextKey) (ContextTarget, error) {
		return ContextTarget{Instance: in.ID, Project: "acme/shop", Revision: "1"}, nil
	})
	c := NewContextService(New(Deps{Config: cfg, Clients: map[corehosts.Kind]HostClient{corehosts.KindGitHub: client}}), resolve)
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err := c.Read(ctx, corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssues})
		cancel()
		if err != nil || out.RetryAt != client.until {
			t.Fatal("lost rate-limit deadline", out, err)
		}
	}
	if client.reads.Load() != 1 {
		t.Fatal("retry reached host before deadline")
	}
}
