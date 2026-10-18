package repos_test

import (
	"context"
	"sync/atomic"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
)

type countPub struct{ n atomic.Int32 }

func (p *countPub) PublishReposChanged() { p.n.Add(1) }

// The service, not the HTTP handler, publishes: every caller (the API, the
// seed command) gets the message, and exactly once per change.
func TestServicePublishesOnceAfterEachChange(t *testing.T) {
	e := newEnv(t)
	e.repoDir("shop-api")
	ctx := context.Background()

	tracked := e.track("acme-shop", "shop-api").(corerepos.Tracked)
	if got := e.pub.n.Load(); got != 1 {
		t.Fatalf("publishes after track = %d, want 1", got)
	}
	// Nothing changes: no message.
	if _, ok := e.track("acme-shop", "shop-api").(corerepos.AlreadyTracked); !ok {
		t.Fatal("tracking the same repo again changed something")
	}
	if got := e.pub.n.Load(); got != 1 {
		t.Errorf("publishes after a no-op track = %d, want 1", got)
	}
	// Untrack the only repo, then the project that no longer has an active repo:
	// the project still disappears from the list, so subscribers must hear it.
	if _, err := e.svc.UntrackRepo(ctx, tracked.Added[0], "user"); err != nil {
		t.Fatal(err)
	}
	if got := e.pub.n.Load(); got != 2 {
		t.Errorf("publishes after untrack repo = %d, want 2", got)
	}
	if _, err := e.svc.UntrackProject(ctx, tracked.ProjectID, "user"); err != nil {
		t.Fatal(err)
	}
	if got := e.pub.n.Load(); got != 3 {
		t.Errorf("publishes after untracking a project with no active repos = %d, want 3", got)
	}
	// A refused change publishes nothing.
	if _, err := e.svc.UntrackProject(ctx, tracked.ProjectID, "user"); err != nil {
		t.Fatal(err)
	}
	if got := e.pub.n.Load(); got != 3 {
		t.Errorf("publishes after a refused untrack = %d, want 3", got)
	}
}
