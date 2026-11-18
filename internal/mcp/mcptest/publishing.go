package mcptest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
)

// PublishTemplate is what a FakePublishing resolves for a publishable repo: the
// values the server owns.
type PublishTemplate struct {
	Operation    core.OperationID
	Branch       string
	ExpectedHead string
	Marker       string
}

// BoundPublish is one Bind call.
type BoundPublish struct {
	Plan     mcp.PublishPlan
	Proposal core.ProposalID
}

// FakePublishing is an in-memory mcp.Publishing. Repos lists the publishable
// repos; any other repo is ErrNotPublishable. Problems, PlanErr and BindErr make
// the matching call report them.
type FakePublishing struct {
	mu       sync.Mutex
	Repos    map[core.RepoID]PublishTemplate
	Problems []string
	PlanErr  error
	BindErr  error
	// Planned and Bound record the calls.
	Planned []mcp.PublishRequest
	Bound   []BoundPublish
}

var _ mcp.Publishing = (*FakePublishing)(nil)

// NewFakePublishing returns a fake that can publish the given repos.
func NewFakePublishing(repos ...core.RepoID) *FakePublishing {
	f := &FakePublishing{Repos: map[core.RepoID]PublishTemplate{}}
	for _, r := range repos {
		f.Repos[r] = PublishTemplate{
			Operation: core.OperationID("op_" + string(r)), Branch: "main",
			ExpectedHead: strings.Repeat("ab", 20), Marker: strings.Repeat("c", 32),
		}
	}
	return f
}

// Plan implements mcp.Publishing. It reads and records only.
func (f *FakePublishing) Plan(_ context.Context, repo core.RepoID, req mcp.PublishRequest) (mcp.PublishPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Planned = append(f.Planned, req)
	if f.PlanErr != nil {
		return mcp.PublishPlan{}, f.PlanErr
	}
	t, ok := f.Repos[repo]
	if !ok {
		return mcp.PublishPlan{}, mcp.ErrNotPublishable
	}
	vis := req.Visibility
	if vis == "" {
		vis = "private"
	}
	return mcp.PublishPlan{
		Operation: t.Operation, Repo: repo, Host: req.Host, Owner: req.Owner, OwnerKind: req.OwnerKind, Name: req.Name,
		Visibility: vis, Branch: t.Branch, ExpectedHead: t.ExpectedHead, Marker: t.Marker,
		Problems: append([]string(nil), f.Problems...),
	}, nil
}

// Bind implements mcp.Publishing.
func (f *FakePublishing) Bind(_ context.Context, plan mcp.PublishPlan, proposal core.ProposalID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BindErr != nil {
		return f.BindErr
	}
	f.Bound = append(f.Bound, BoundPublish{Plan: plan, Proposal: proposal})
	return nil
}

// PublishingHarness is what PublishingContract needs: the implementation, one
// repo it can publish and one it cannot.
type PublishingHarness struct {
	Pub         mcp.Publishing
	Publishable core.RepoID
	Other       core.RepoID
}

// PublishingContract is the contract of mcp.Publishing. The fake and the real
// adapter must both pass it.
func PublishingContract(t *testing.T, mk func(*testing.T) PublishingHarness) {
	t.Helper()
	req := mcp.PublishRequest{Host: "git.acme.dev", Owner: "acme", OwnerKind: "org", Name: "shop-new", Visibility: "private"}
	t.Run("a repo that is not publishable is ErrNotPublishable", func(t *testing.T) {
		h := mk(t)
		if _, err := h.Pub.Plan(context.Background(), h.Other, req); !errors.Is(err, mcp.ErrNotPublishable) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the plan carries the destination and the server's values", func(t *testing.T) {
		h := mk(t)
		p, err := h.Pub.Plan(context.Background(), h.Publishable, req)
		if err != nil {
			t.Fatal(err)
		}
		if p.Repo != h.Publishable || p.Operation == "" || p.Host != req.Host || p.Owner != req.Owner ||
			p.OwnerKind != req.OwnerKind || p.Name != req.Name || p.Visibility != "private" {
			t.Fatalf("plan %+v", p)
		}
		if p.Branch != "main" || (len(p.ExpectedHead) != 40 && len(p.ExpectedHead) != 64) || len(p.Marker) < 16 || len(p.Marker) > 64 {
			t.Fatalf("server values %+v", p)
		}
	})
	t.Run("planning twice gives the same plan", func(t *testing.T) {
		h := mk(t)
		a, _ := h.Pub.Plan(context.Background(), h.Publishable, req)
		b, _ := h.Pub.Plan(context.Background(), h.Publishable, req)
		if a.Marker != b.Marker || a.ExpectedHead != b.ExpectedHead || a.Operation != b.Operation {
			t.Fatalf("plans differ: %+v %+v", a, b)
		}
	})
	t.Run("a visibility left out is private", func(t *testing.T) {
		h := mk(t)
		r := req
		r.Visibility = ""
		p, err := h.Pub.Plan(context.Background(), h.Publishable, r)
		if err != nil || p.Visibility != "private" {
			t.Fatalf("%+v %v", p, err)
		}
	})
}
