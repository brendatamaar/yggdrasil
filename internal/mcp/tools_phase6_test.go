package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/mcp/mcptest"
)

// The Phase 6 proposal tools (MCP-05, NR-04, WS-09, GH-06, MCP-06): an agent
// may only create pending proposals, for repos in its scope, and never an
// InitRepo.

var phase6Tools = []string{"propose_publish", "propose_review_comment", "propose_ignore"}

var testHead = strings.Repeat("ab", 20)

func phase6Args(tool, repo string) map[string]any {
	switch tool {
	case "propose_publish":
		return map[string]any{"repo": repo, "host": "git.acme.dev", "owner": "acme", "owner_kind": "org", "name": "shop-new"}
	case "propose_review_comment":
		return map[string]any{"repo": repo, "mr": 4, "expected_head": testHead, "body": "Please check the nil case."}
	}
	return map[string]any{"repo": repo, "entries": []string{"/dist/"}}
}

func TestPhase6ToolsAreOffered(t *testing.T) {
	e := newEnv(t)
	tools := e.listTools(t)
	for _, tool := range phase6Tools {
		if !slices.Contains(tools, tool) {
			t.Errorf("%s not offered", tool)
		}
	}
	for _, bad := range []string{"propose_init_repo", "init_repo", "propose_subtasks", "confirm_proposal"} {
		if slices.Contains(tools, bad) {
			t.Errorf("%s offered", bad)
		}
	}
}

// Ask mode: the three tools stay offered (Ask tasks draft comments and ignore
// proposals), and calling them creates only a pending proposal: no git, build
// or read effect, so no tree changes.
func TestPhase6ToolsInAskModeOnlyPropose(t *testing.T) {
	e := newEnv(t)
	e.ws.Set(e.task, snapshot(core.ModeAsk))
	for _, tool := range phase6Tools {
		if !slices.Contains(e.listTools(t), tool) {
			t.Fatalf("%s not offered in Ask mode", tool)
		}
		r := e.callTool(t, tool, phase6Args(tool, "shop-api"))
		if r.Result.IsError || !strings.Contains(r.text(), "Nothing has been done yet") {
			t.Fatalf("%s in Ask mode: %s", tool, r.Raw)
		}
	}
	if len(e.prop.Created) != 3 {
		t.Fatalf("proposals: %d", len(e.prop.Created))
	}
	if n := len(e.git.All()) + len(e.bl.All()) + len(e.rd.All()); n != 0 {
		t.Fatalf("a propose tool had an effect: git %v builds %v reads %v", e.git.All(), e.bl.All(), e.rd.All())
	}
}

// MCP-06 and invariant 6: untracked, unknown and out-of-scope repos get the
// identical refusal, and nothing is planned, audited or created.
func TestPhase6ToolsCannotReachAnUntrackedRepo(t *testing.T) {
	e := newEnv(t)
	for _, tool := range phase6Tools {
		var answers []string
		// vault is untracked (not in the snapshot); shop-admin is visible but out of scope.
		for _, repo := range []string{"vault", "r-vault", "shop-admin", "r-admin", "SHOP-ADMIN", "nope"} {
			r := e.callTool(t, tool, phase6Args(tool, repo))
			if !r.Result.IsError {
				t.Fatalf("%s served for %q: %s", tool, repo, r.Raw)
			}
			answers = append(answers, r.text())
		}
		for _, a := range answers[1:] {
			if a != answers[0] {
				t.Errorf("%s: answers differ (%q vs %q): the agent could probe", tool, answers[0], a)
			}
		}
	}
	if n := len(e.prop.Created) + e.audit.Count() + len(e.pub.Planned) + len(e.pub.Bound); n != 0 {
		t.Fatalf("%d effects ran for hidden repos", n)
	}
}

// A repo untracked mid-run disappears at the next call.
func TestPhase6ToolsFollowUntracking(t *testing.T) {
	e := newEnv(t)
	for _, tool := range []string{"propose_ignore", "propose_review_comment"} {
		if r := e.callTool(t, tool, phase6Args(tool, "shop-web")); r.Result.IsError {
			t.Fatalf("%s: %s", tool, r.Raw)
		}
	}
	e.ws.Untrack(webRepo)
	for _, tool := range []string{"propose_ignore", "propose_review_comment"} {
		if r := e.callTool(t, tool, phase6Args(tool, "shop-web")); !r.Result.IsError {
			t.Fatalf("%s served after untrack: %s", tool, r.Raw)
		}
	}
}

func TestPhase6ToolsNeedAScopeRepo(t *testing.T) {
	e := newEnv(t)
	s := snapshot(core.ModeCode)
	for i := range s.Repos {
		s.Repos[i].InScope = false
	}
	e.ws.Set(e.task, s)
	tools := e.listTools(t)
	for _, tool := range phase6Tools {
		if slices.Contains(tools, tool) {
			t.Errorf("%s offered with an empty scope", tool)
		}
		if r := e.callTool(t, tool, phase6Args(tool, "shop-api")); r.Err == nil && !r.Result.IsError {
			t.Errorf("%s served with an empty scope: %s", tool, r.Raw)
		}
	}
	if len(e.prop.Created) != 0 {
		t.Fatal("a proposal was created")
	}
}

// Each call is audited once, before the proposal, without the comment text or
// the ignore entries; no secret reaches the proposal or the audit entry.
func TestPhase6AuditAndTokens(t *testing.T) {
	e := newEnv(t)
	secret := e.token
	r := e.callTool(t, "propose_review_comment", map[string]any{
		"repo": "shop-api", "mr": 4, "expected_head": testHead,
		"body": "Found " + secret + " in C:\\Users\\dev\\work\\shop-api\\.env and /home/dev/work/shop-api/.env",
	})
	if r.Result.IsError {
		t.Fatal(r.Raw)
	}
	for _, tool := range []string{"propose_ignore", "propose_publish"} {
		a := phase6Args(tool, "shop-api")
		a["summary"] = "token " + secret
		if r := e.callTool(t, tool, a); r.Result.IsError {
			t.Fatalf("%s: %s", tool, r.Raw)
		}
	}
	if e.audit.Count() != 3 {
		t.Fatalf("audit entries: %d, want 3", e.audit.Count())
	}
	c := e.prop.Created[0].Action.(coreprop.ReviewComment)
	for _, bad := range []string{secret, "C:\\Users", "/home/dev"} {
		if strings.Contains(c.Body, bad) {
			t.Errorf("the stored comment still holds %q: %s", bad, c.Body)
		}
	}
	if !strings.Contains(c.Body, "Found") {
		t.Errorf("the comment lost its text: %q", c.Body)
	}
	for _, rec := range e.audit.Records {
		b, _ := json.Marshal(rec)
		for _, bad := range []string{secret, "nil case", "/dist/", "Found"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("audit entry holds %q: %s", bad, b)
			}
		}
		if rec.Subject != "repo:r-api" {
			t.Errorf("subject %q", rec.Subject)
		}
	}
	// The summary is redacted by the proposals service, not here, but the result
	// text the agent sees never echoes the token either.
	if strings.Contains(r.Raw, secret) {
		t.Error("the answer holds the token")
	}
}

// A failed audit write means the proposal is not created (invariant 8).
func TestPhase6ToolsNeedTheAuditEntry(t *testing.T) {
	e := newEnv(t)
	e.audit.Fail = true
	for _, tool := range phase6Tools {
		if r := e.callTool(t, tool, phase6Args(tool, "shop-api")); !r.Result.IsError {
			t.Fatalf("%s ran without an audit entry: %s", tool, r.Raw)
		}
	}
	if len(e.prop.Created) != 0 || len(e.pub.Bound) != 0 {
		t.Fatal("a proposal was created without an audit entry")
	}
}

func TestPhase6ParseRefusalsCreateNothing(t *testing.T) {
	e := newEnv(t)
	bad := []struct {
		tool string
		args map[string]any
	}{
		{"propose_ignore", map[string]any{"repo": "shop-api", "entries": []string{"*"}}},
		{"propose_ignore", map[string]any{"repo": "shop-api", "entries": []string{"/a/../b/"}}},
		{"propose_ignore", map[string]any{"repo": "shop-api", "entries": []string{}}},
		{"propose_review_comment", map[string]any{"repo": "shop-api", "mr": 4, "expected_head": "main", "body": "x"}},
		{"propose_review_comment", map[string]any{"repo": "shop-api", "mr": 4, "expected_head": testHead, "body": strings.Repeat("x", 16<<10+1)}},
		{"propose_review_comment", map[string]any{"repo": "shop-api", "mr": 4, "expected_head": testHead, "body": "x", "marker": "abcdefabcdefabcdef"}},
		{"propose_publish", map[string]any{"repo": "shop-api", "host": "git.acme.dev", "owner": "acme", "owner_kind": "team", "name": "x"}},
		{"propose_publish", map[string]any{"repo": "shop-api", "host": "git.acme.dev", "owner": "acme", "owner_kind": "org", "name": "x", "operation": "op_9"}},
		{"propose_publish", map[string]any{"repo": "shop-api", "host": "git.acme.dev", "owner": "acme", "owner_kind": "org", "name": "x", "branch": "dev"}},
	}
	for _, b := range bad {
		r := e.callTool(t, b.tool, b.args)
		if r.Err == nil && !r.Result.IsError {
			t.Errorf("%s accepted %v: %s", b.tool, b.args, r.Raw)
		}
	}
	if n := len(e.prop.Created) + e.audit.Count() + len(e.pub.Planned); n != 0 {
		t.Fatalf("%d effects ran for refused calls", n)
	}
}

func TestReviewCommentMarkersAreFreshAndHex(t *testing.T) {
	e := newEnv(t)
	seen := map[string]bool{}
	for range 5 {
		if r := e.callTool(t, "propose_review_comment", phase6Args("propose_review_comment", "shop-api")); r.Result.IsError {
			t.Fatal(r.Raw)
		}
	}
	for _, p := range e.prop.Created {
		m := p.Action.(coreprop.ReviewComment).Marker
		if len(m) != 32 || strings.Trim(m, "0123456789abcdef") != "" || seen[m] {
			t.Fatalf("marker %q (seen %v)", m, seen[m])
		}
		seen[m] = true
		if err := p.Action.Validate(); err != nil {
			t.Fatalf("the comment action is not valid: %v", err)
		}
	}
}

func TestIgnoreProposalTargetsTheMainCheckoutOnly(t *testing.T) {
	e := newEnv(t)
	// shop-api has a task worktree; the ignore proposal still names the main checkout.
	if r := e.callTool(t, "propose_ignore", map[string]any{"repo": "shop-api", "entries": []string{"/dist/", "/web/node_modules/"}}); r.Result.IsError {
		t.Fatal(r.Raw)
	}
	a := e.prop.Created[0].Action.(coreprop.IgnoreEntries)
	if a.Where != (coreprop.MainCheckout{Repo: apiRepo}) || a.ExpectedHash != "" || a.ExpectedAbsent {
		t.Fatalf("action %+v: the checksum is pinned by the proposals service, not by the agent", a)
	}
}

// --- propose_publish ---

func TestPublishIsNotOfferedWithoutThePublishingService(t *testing.T) {
	e := newEnv(t)
	e.withPublishing(t, nil)
	if slices.Contains(e.listTools(t), "propose_publish") {
		t.Fatal("propose_publish offered without a publishing service")
	}
	if r := e.callTool(t, "propose_publish", phase6Args("propose_publish", "shop-api")); r.Err == nil && !r.Result.IsError {
		t.Fatalf("served: %s", r.Raw)
	}
}

func TestPublishBuildsTheActionFromServerValues(t *testing.T) {
	e := newEnv(t)
	r := e.callTool(t, "propose_publish", map[string]any{
		"repo": "shop-api", "host": "git.acme.dev", "owner": "acme", "owner_kind": "org", "name": "shop-new", "visibility": "public",
	})
	if r.Result.IsError {
		t.Fatal(r.Raw)
	}
	a := e.prop.Created[0].Action.(coreprop.PublishRepo)
	want := coreprop.PublishRepo{Operation: "op_r-api", Repo: apiRepo, HostInstance: "git.acme.dev", Owner: "acme", OwnerKind: "org",
		Name: "shop-new", Visibility: "public", Branch: "main", ExpectedHead: testHead, Marker: strings.Repeat("c", 32)}
	if a != want {
		t.Fatalf("action %+v, want %+v", a, want)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(e.pub.Bound) != 1 || e.pub.Bound[0].Proposal != "prp_1" || e.pub.Bound[0].Plan.Marker != a.Marker {
		t.Fatalf("bound %+v", e.pub.Bound)
	}
	if len(e.pub.Planned) != 1 || e.pub.Planned[0].Visibility != "public" {
		t.Fatalf("planned %+v", e.pub.Planned)
	}
}

func TestPublishRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{"not a new repo", func(e *env) { e.pub.Repos = map[core.RepoID]mcptest.PublishTemplate{} }, "cannot be published from here"},
		{"account problem", func(e *env) {
			e.pub.Problems = []string{"That account is not an organization you can create repositories under."}
		}, "cannot be published yet: That account"},
		{"service error", func(e *env) { e.pub.PlanErr = errors.New("host exploded with secret-ish detail") }, "That tool failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			r := e.callTool(t, "propose_publish", phase6Args("propose_publish", "shop-api"))
			if !r.Result.IsError || !strings.Contains(r.text(), tc.want) {
				t.Fatalf("answer: %s", r.Raw)
			}
			if strings.Contains(r.text(), "secret-ish") {
				t.Error("the internal error reached the agent")
			}
			if len(e.prop.Created) != 0 || len(e.pub.Bound) != 0 || e.audit.Count() != 0 {
				t.Fatal("something was created or audited for a refused publish")
			}
		})
	}
}

func TestPublishBindFailureIsReported(t *testing.T) {
	e := newEnv(t)
	e.pub.BindErr = errors.New("pending map is full")
	r := e.callTool(t, "propose_publish", phase6Args("propose_publish", "shop-api"))
	if !r.Result.IsError || !strings.Contains(r.text(), "could not be prepared for confirmation") || !strings.Contains(r.text(), "prp_1") {
		t.Fatalf("answer: %s", r.Raw)
	}
}

// A plan the service resolved for another repo is never turned into a proposal.
func TestPublishPlanForAnotherRepoIsRefused(t *testing.T) {
	e := newEnv(t)
	e.withPublishing(t, wrongRepoPublishing{e.pub})
	r := e.callTool(t, "propose_publish", phase6Args("propose_publish", "shop-api"))
	if !r.Result.IsError || len(e.prop.Created) != 0 {
		t.Fatalf("answer: %s", r.Raw)
	}
}

type wrongRepoPublishing struct{ *mcptest.FakePublishing }

func (w wrongRepoPublishing) Plan(ctx context.Context, repo core.RepoID, req mcp.PublishRequest) (mcp.PublishPlan, error) {
	p, err := w.FakePublishing.Plan(ctx, repo, req)
	p.Repo = "r-other"
	return p, err
}

// The agent-facing result of a publish proposal says it is pending and that
// nothing was done: the proposal only exists.
func TestPublishAnswerSaysNothingHappened(t *testing.T) {
	e := newEnv(t)
	r := e.callTool(t, "propose_publish", phase6Args("propose_publish", "shop-api"))
	if !strings.Contains(r.text(), "Nothing has been done yet") || !strings.Contains(r.text(), "status pending") {
		t.Fatalf("answer: %s", r.Raw)
	}
}

// The Proposer fake mirrors the proposals service: an InitRepo from an agent is
// refused. The server cannot build one in any case: no call variant makes it.
func TestNoToolCanCreateAnInitRepo(t *testing.T) {
	e := newEnv(t)
	for _, tool := range e.listTools(t) {
		if strings.Contains(tool, "init") {
			t.Errorf("tool %s", tool)
		}
	}
	for _, tool := range phase6Tools {
		e.callTool(t, tool, phase6Args(tool, "shop-api"))
	}
	for _, p := range e.prop.Created {
		if _, ok := p.Action.(coreprop.InitRepo); ok {
			t.Fatalf("an InitRepo was proposed: %+v", p)
		}
		if p.Action.Kind() == core.ProposalInitRepo {
			t.Fatalf("an init proposal: %+v", p)
		}
	}
	_, err := e.prop.Create(context.Background(), e.task, coreprop.InitRepo{}, "")
	if err == nil {
		t.Fatal("the fake proposer accepted an InitRepo")
	}
}

func TestPublishingContractAgainstTheFake(t *testing.T) {
	mcptest.PublishingContract(t, func(*testing.T) mcptest.PublishingHarness {
		return mcptest.PublishingHarness{Pub: mcptest.NewFakePublishing("r-1"), Publishable: "r-1", Other: "r-2"}
	})
}
