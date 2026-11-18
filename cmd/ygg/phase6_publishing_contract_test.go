package main

import (
	"context"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mcp/mcptest"
)

// mcp.Publishing over the real new-repo service passes the same contract as the
// fake (MCP-05), and planning calls no host write.
func TestMCPPublishingContractOnTheRealService(t *testing.T) {
	r := newNRRig(t)
	ready := r.initRepo("shop-contract")
	repos, err := r.a.store.ListRepos(context.Background(), r.project)
	if err != nil {
		t.Fatal(err)
	}
	var other core.RepoID
	for _, rp := range repos {
		if rp.ID != ready.RepoID {
			other = rp.ID
		}
	}
	if other == "" {
		t.Fatal("no second repo to use as the not publishable one")
	}
	mcptest.PublishingContract(t, func(*testing.T) mcptest.PublishingHarness {
		return mcptest.PublishingHarness{
			Pub:         mcpPublishing{svc: r.a.phase6.nr.svc, store: r.a.store},
			Publishable: ready.RepoID, Other: other,
		}
	})
	if r.host.effects() != 0 {
		t.Fatalf("planning called the host: %v", r.host.calls)
	}
}
