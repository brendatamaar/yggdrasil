package mcp_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
)

// revokingBuilds ends the session from inside a service call that runs after
// Authorize and before the audit-and-effect step.
type revokingBuilds struct {
	mcp.Builds
	revoke func()
}

func (b revokingBuilds) Presets(ctx context.Context, p core.ProjectID) ([]mcp.BuildPreset, error) {
	out, err := b.Builds.Presets(ctx, p)
	b.revoke()
	return out, err
}

func (b revokingBuilds) InstallStep(ctx context.Context, r core.RepoID) (string, error) {
	out, err := b.Builds.InstallStep(ctx, r)
	b.revoke()
	return out, err
}

func (e *env) withBuilds(t *testing.T, mk func(mcp.Builds) mcp.Builds) {
	t.Helper()
	srv, err := mcp.New(mcp.Config{
		Workspace: e.ws, Reader: e.rd, Builds: mk(e.bl), Git: e.git, Hosts: e.hosts, DevServers: mcp.NoDevServers{},
		Proposer: e.prop, Audit: e.audit, Redactor: e.redact, Sessions: e.srv.Sessions(),
		Clock:  func() time.Time { return time.Unix(0, e.now.Load()).UTC() },
		Logger: slog.New(slog.NewTextHandler(&e.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	e.http.Close()
	e.http = httptest.NewServer(srv.Handler())
	t.Cleanup(e.http.Close)
}

// A session revoked between Authorize and the effect gets no audit entry and
// no effect.
func TestRevokeBetweenAuthorizeAndEffect(t *testing.T) {
	e := newEnv(t)
	e.bl.PresetList = []mcp.BuildPreset{{ID: "pre1", Name: "web+api", Repos: []core.RepoID{webRepo, apiRepo}}}
	e.withBuilds(t, func(b mcp.Builds) mcp.Builds {
		return revokingBuilds{Builds: b, revoke: func() { e.srv.Sessions().Revoke("t1") }}
	})
	r := e.callTool(t, "run_build_preset", map[string]any{"preset": "web+api"})
	if !r.Result.IsError || !strings.Contains(r.text(), "ended") {
		t.Fatalf("revoked preset call: %s", r.Raw)
	}
	if e.audit.Count() != 0 || len(e.bl.All()) != 0 {
		t.Fatalf("effect after revoke: audit %d, builds %v", e.audit.Count(), e.bl.All())
	}

	e2 := newEnv(t)
	e2.withBuilds(t, func(b mcp.Builds) mcp.Builds {
		return revokingBuilds{Builds: b, revoke: func() { e2.srv.Sessions().Revoke("t1") }}
	})
	r = e2.callTool(t, "propose_install", map[string]any{"repo": "shop-api", "summary": "s"})
	if !r.Result.IsError {
		t.Fatalf("revoked propose call: %s", r.Raw)
	}
	if e2.audit.Count() != 0 || len(e2.prop.Created) != 0 {
		t.Fatalf("proposal after revoke: audit %d, created %d", e2.audit.Count(), len(e2.prop.Created))
	}
}
