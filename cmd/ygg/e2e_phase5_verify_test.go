package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// Fresh worktrees of a uv (Python) repo and of the .NET repo verify to green
// through the real services: a Code-mode task (mock agent) gets its worktree
// from Yggdrasil, and the verification that follows the turn bootstraps it
// (uv sync, dotnet restore) and runs the verify chain.
//
//   - shop-inventory on its chore/bump-deps branch (the second venv state of
//     the fixture): the toolchain's own default chain (uv run pytest). The
//     fixture's main checkout is mid-merge, so the test puts the temporary
//     copy on that branch first.
//   - shop-ledger: the .NET tests are a console program, so the verify command
//     is `dotnet run --project tests/Ledger.Tests` (restore, build, run).
//
// Both need a toolchain and, for uv, the network (fastapi and httpx are
// downloaded), so each skips itself when its tool is missing. The Windows CI
// runner installs both.
func testP5UvAndDotnetVerify(t *testing.T) {
	t.Run("uv", func(t *testing.T) {
		p5NeedTool(t, "go", "uv")
		cfg := p5VerifyConfig(t)
		p := newP5Project(t, nil, []string{"shop-inventory"}, cfg)
		repoDir := filepath.Join(p.dir, "shop-inventory")
		searchtest.RunGit(t, p.env, repoDir, "merge", "--abort")
		searchtest.RunGit(t, p.env, repoDir, "checkout", "-q", "chore/bump-deps")
		p5VerifyGreen(t, p, "shop-inventory", cfg)
	})
	t.Run("dotnet", func(t *testing.T) {
		p5NeedTool(t, "go", "dotnet")
		cfg := p5VerifyConfig(t)
		p := newP5Project(t, nil, []string{"shop-ledger"}, cfg)
		p.setVerify("shop-ledger", "dotnet", "run", "--project", filepath.Join("tests", "Ledger.Tests"))
		p5VerifyGreen(t, p, "shop-ledger", cfg)
	})
}

// p5VerifyGreen runs a Code-mode task on repo and requires its verification to
// pass in the task's fresh worktree.
func p5VerifyGreen(t *testing.T, p *p5Project, repo string, cfg config) {
	t.Helper()
	a, base := startFullApp(t, cfg)
	c := &p5Client{t: t, base: base, client: appClient}
	var created struct{ Task core.Task }
	if code, data := c.do(http.MethodPost, "/api/tasks", api.CreateTaskRequest{
		ProjectID: p.project, RepoIDs: []core.RepoID{p.repos[repo]}, Scope: "repo", OptIn: true,
		Title: "Verify " + repo, Prompt: "Look at the repo.", Mode: core.ModeCode, Agent: "mock",
	}, &created); code >= 300 {
		t.Fatalf("create task: %d %s", code, data)
	}
	id := created.Task.ID

	var wts api.WorktreesResponse
	eventually(t, 60*time.Second, "the task's worktree", func() bool {
		c.do(http.MethodGet, "/api/tasks/"+string(id)+"/worktrees", nil, &wts)
		return len(wts.Worktrees) == 1
	})
	// The API hides folders, so the path comes from the store.
	stored, err := a.store.ListWorktrees(context.Background(), id)
	if err != nil || len(stored) != 1 {
		t.Fatalf("worktrees of the task: %v, %v", stored, err)
	}
	wtDir := stored[0].Path
	if _, err := os.Stat(filepath.Join(wtDir, ".venv")); err == nil {
		t.Fatal("the worktree is not fresh: it already has a .venv")
	}

	// The mock agent edits nothing, so (as in the Phase 4 test) the edit lands
	// while its turn runs; a repo without changes is skipped, not verified.
	note := filepath.Join(wtDir, "NOTES.md")
	if err := os.WriteFile(note, []byte("# notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var vs api.VerificationsResponse
	var got api.VerificationDTO
	deadline := time.Now().Add(8 * time.Minute) // a cold uv sync or dotnet restore downloads packages
	for time.Now().Before(deadline) {
		c.do(http.MethodGet, "/api/tasks/"+string(id)+"/verifications", nil, &vs)
		for _, v := range vs.Verifications {
			if v.RepoID == p.repos[repo] {
				got = v
			}
		}
		switch got.State {
		case api.VerifyPassed:
			return
		case api.VerifyFailed, api.VerifyUnverifiable, api.VerifyInterrupted, api.VerifyCancelled:
			t.Fatalf("verification of %s ended %q (reason %q) at stage %q step %q: %s", repo, got.State, got.Reason, got.Stage, got.FailedStep, strings.TrimSpace(got.Message))
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("verification of %s did not finish: %+v", repo, got)
}

// p5VerifyConfig paces the mock agent (300 ms per event) so the edit made
// above lands while its turn runs.
func p5VerifyConfig(t *testing.T) config {
	return config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true, mockDelay: 300 * time.Millisecond}
}
