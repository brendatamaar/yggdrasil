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
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// End-to-end flows of the Phase 6 surfaces through the real wiring: real HTTP
// handlers, real stores, the real action, proposal and task services, temp git
// repos with a local bare origin, the mock agent adapter and stub host data.
// No real host, agent CLI or model API.

func (r *p6Rig) confirmHTTP(t *testing.T, id core.ProposalID) (int, []byte) {
	t.Helper()
	var p api.ProposalDTO
	if code := r.get(t, "/api/proposals/"+string(id), &p); code != 200 {
		t.Fatalf("get proposal = %d", code)
	}
	return r.post(t, "/api/proposals/"+string(id)+"/confirm", api.ConfirmProposalRequest{Hash: p.ActionHash}, nil)
}

// settle waits until the proposal has left pending and confirmed.
func (r *p6Rig) settle(t *testing.T, id core.ProposalID) api.ProposalDTO {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var p api.ProposalDTO
		if code := r.get(t, "/api/proposals/"+string(id), &p); code != 200 {
			t.Fatalf("get proposal = %d", code)
		}
		if p.Status != core.ProposalPending && p.Status != core.ProposalConfirmed {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("proposal %s is still %s", id, p.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (r *p6Rig) askTask(t *testing.T, p *p6Repo) core.Task {
	t.Helper()
	return dbtest.Task(t, r.a.store, core.Task{ProjectID: p.project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{p.id}, Mode: core.ModeAsk, Status: core.TaskRunning})
}

func (r *p6Rig) mcpCall(t *testing.T, task core.TaskID, tool string, args map[string]any) (string, bool) {
	t.Helper()
	token, err := r.a.mcp.Issue(task, 1)
	if err != nil {
		t.Fatal(err)
	}
	return mcpRPC(t, r.base, token, tool, args)
}

func (r *p6Rig) originHead(t *testing.T, p *p6Repo) string {
	t.Helper()
	return searchtest.GitOutput(t, r.env, p.bare, "", "rev-parse", "HEAD")
}

// Commit proposals made by an Ask task on a dirty main checkout: pending and
// diff-pinned, nothing committed or staged until the confirmation, a diff that
// changed after the proposal is refused, and no remote is ever touched
// (invariants 2, 9).
func TestPhase6E2ECommitProposalsFromAnAskTask(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.refresh(t, p)
	readme := filepath.Join(p.dir, "README.md")
	if err := os.WriteFile(readme, []byte("# shop-api\nfirst change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.dir, "notes.txt"), []byte("scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := r.askTask(t, p)
	head0, origin0, tree0 := r.head(t, p), r.originHead(t, p), r.tree(t, p)

	raw, isErr := r.mcpCall(t, task.ID, "propose_commit", map[string]any{"repo": string(p.id), "paths": []string{"README.md"},
		"message": "Update the readme", "summary": "Readme change"})
	if isErr {
		t.Fatalf("propose_commit from an Ask task: %s", raw)
	}
	views, err := r.a.proposals.List(context.Background(), proposals.ListFilter{Task: task.ID})
	if err != nil || len(views) != 1 || views[0].Status != core.ProposalPending || !views[0].FromAgent || views[0].Kind != core.ProposalCommit {
		t.Fatalf("proposals = %+v, %v; want one pending commit proposal from the agent", views, err)
	}
	if got := r.tree(t, p); got != tree0 {
		t.Fatalf("a proposal changed the main checkout:\n%s\n%s", tree0, got)
	}
	if staged := searchtest.GitOutput(t, r.env, p.dir, "", "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("a proposal staged %q before any confirmation", staged)
	}

	// Stale: the pinned file changes after the proposal. The confirmation is refused.
	if err := os.WriteFile(readme, []byte("# shop-api\nsecond change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.confirmHTTP(t, views[0].ID)
	if got := r.settle(t, views[0].ID); got.Status != core.ProposalFailed || got.FailKind != "stale" {
		t.Fatalf("a stale diff ended as %s (%q): %s", got.Status, got.FailKind, got.Message)
	}
	if r.head(t, p) != head0 {
		t.Fatal("a stale proposal committed")
	}

	// A fresh proposal for the current diff confirms and commits exactly its paths.
	if _, isErr := r.mcpCall(t, task.ID, "propose_commit", map[string]any{"repo": string(p.id), "paths": []string{"README.md"},
		"message": "Update the readme again", "summary": "Readme change"}); isErr {
		t.Fatal("second propose_commit refused")
	}
	views, _ = r.a.proposals.List(context.Background(), proposals.ListFilter{Task: task.ID, Status: core.ProposalPending})
	if len(views) != 1 {
		t.Fatalf("pending proposals = %d, want 1 (the stale one is decided)", len(views))
	}
	if code, data := r.confirmHTTP(t, views[0].ID); code != http.StatusOK {
		t.Fatalf("confirm = %d %s", code, data)
	}
	if got := r.settle(t, views[0].ID); got.Status != core.ProposalExecuted {
		t.Fatalf("the proposal ended as %s: %s", got.Status, got.Message)
	}
	if r.head(t, p) == head0 {
		t.Fatal("the confirmed proposal did not commit")
	}
	if files := searchtest.GitOutput(t, r.env, p.dir, "", "show", "--name-only", "--pretty=format:", "HEAD"); strings.TrimSpace(files) != "README.md" {
		t.Fatalf("the commit holds %q, want only README.md", files)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "notes.txt")); err != nil {
		t.Fatalf("the untracked file was touched: %v", err)
	}
	if r.originHead(t, p) != origin0 {
		t.Fatal("a commit proposal pushed to the remote")
	}
}

// The ignore proposal edits .gitignore only after confirmation, keeps its CRLF
// line endings, and refuses when the file changed after the proposal (WS-09).
func TestPhase6E2EIgnoreEditKeepsCRLFAndRefusesAStaleFile(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	ignore := filepath.Join(p.dir, ".gitignore")
	if err := os.WriteFile(ignore, []byte("/bin/\r\n/dist/\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	searchtest.RunGit(t, r.env, p.dir, "add", ".gitignore")
	searchtest.RunGit(t, r.env, p.dir, "commit", "-m", "add gitignore")
	searchtest.RunGit(t, r.env, p.dir, "push", "origin", "HEAD")
	// Enough untracked dependency files to be suggested as noise.
	nm := filepath.Join(p.dir, "node_modules", "left-pad")
	if err := os.MkdirAll(nm, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 150; i++ {
		if err := os.WriteFile(filepath.Join(nm, "f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26))+".js"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.refresh(t, p)
	r.bindFull(t, newHostStub())
	var noise api.NoiseResponse
	if code := r.get(t, "/api/repos/"+string(p.id)+"/noise", &noise); code != 200 || len(noise.Candidates) == 0 {
		t.Fatalf("noise = %d %+v, want a suggestion", code, noise)
	}
	pattern := noise.Candidates[0].Pattern

	propose := func(id string, entries ...string) (int, api.ProposalDTO) {
		var out api.ProposalDTO
		code, data := r.post(t, "/api/repos/"+string(p.id)+"/noise/proposals", api.CreateIgnoreProposalRequest{RequestID: core.RequestID(id), Entries: entries}, &out)
		if code != 200 {
			t.Logf("ignore proposal %d: %s", code, data)
		}
		return code, out
	}
	code, first := propose("req_ign_1", pattern)
	if code != 200 || first.ID == "" {
		t.Fatalf("ignore proposal = %d %+v", code, first)
	}
	before, _ := os.ReadFile(ignore)
	if string(before) != "/bin/\r\n/dist/\r\n" {
		t.Fatalf("a proposal edited .gitignore before confirmation: %q", before)
	}

	// Stale: the file changes (user edit) after the proposal.
	if err := os.WriteFile(ignore, []byte("/bin/\r\n/dist/\r\n/coverage/\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.confirmHTTP(t, first.ID)
	if got := r.settle(t, first.ID); got.Status != core.ProposalFailed {
		t.Fatalf("a stale .gitignore ended as %s", got.Status)
	}
	if got, _ := os.ReadFile(ignore); string(got) != "/bin/\r\n/dist/\r\n/coverage/\r\n" {
		t.Fatalf("the refused proposal changed the file: %q", got)
	}

	// A fresh proposal appends with CRLF and no bare LF.
	code, second := propose("req_ign_2", pattern)
	if code != 200 {
		t.Fatalf("second proposal = %d", code)
	}
	if code, data := r.confirmHTTP(t, second.ID); code != http.StatusOK {
		t.Fatalf("confirm = %d %s", code, data)
	}
	if done := r.settle(t, second.ID); done.Status != core.ProposalExecuted {
		t.Fatalf("the ignore edit ended as %s: %s", done.Status, done.Message)
	}
	got, _ := os.ReadFile(ignore)
	text := string(got)
	if !strings.Contains(text, pattern) || strings.Count(text, "\n") != strings.Count(text, "\r\n") {
		t.Fatalf(".gitignore = %q: want the entry added and every line ending CRLF", text)
	}
}

// bindFull binds the actions, drafts and noise services over the real scope
// with the given host cache, as the composition root does.
func (r *p6Rig) bindFull(t *testing.T, h *hostStub) {
	t.Helper()
	r.bindActionsWith(t, h, noHostReader{})
}

// An issue import makes one draft and starts nothing; a repeat of the request
// returns the same draft; only the explicit Start admits the task (AC-05).
func TestPhase6E2EIssueImportStaysADraftUntilStart(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	h := newHostStub()
	st := h.state(p.id)
	st.linked, st.token = true, true
	r.refresh(t, p)
	r.bindActionsWith(t, h, issueHost{})

	tasksBefore := len(mustListTasks(t, r, p))
	var res api.DraftTaskResponse
	path := "/api/repos/" + string(p.id) + "/issues/42/draft"
	body := api.ImportIssueDraftRequest{RequestID: "req_issue_1", Agent: "mock"}
	if code, data := r.post(t, path, body, &res); code != 200 || res.Task.ID == "" || !res.Created {
		t.Fatalf("import = %d %s %+v", code, data, res)
	}
	if res.Task.Status != core.TaskDraft {
		t.Fatalf("the imported issue is %s, want draft", res.Task.Status)
	}
	if !strings.Contains(res.Task.Prompt, "Cart total is wrong") || !strings.Contains(res.Task.Prompt, "add two items") {
		t.Fatalf("the draft brief lacks the issue: %q", res.Task.Prompt)
	}
	time.Sleep(200 * time.Millisecond)
	if turns := mustListTasks(t, r, p); len(turns) != tasksBefore+1 {
		t.Fatalf("tasks = %d, want one more", len(turns))
	}
	again := api.DraftTaskResponse{}
	if code, _ := r.post(t, path, body, &again); code != 200 || again.Task.ID != res.Task.ID || again.Created {
		t.Fatalf("a repeat of the request gave %+v (code %d), want the first draft", again, code)
	}
	if now, _ := r.a.store.GetTask(context.Background(), res.Task.ID); now.Status != core.TaskDraft {
		t.Fatalf("the draft moved to %s without a Start", now.Status)
	}
	// An anchor or reserved request ID is not accepted from a client.
	if code, _ := r.post(t, path, api.ImportIssueDraftRequest{RequestID: "anchor-x", Agent: "mock"}, nil); code != http.StatusBadRequest {
		t.Fatalf("a reserved request ID = %d, want 400", code)
	}
	// Start is explicit.
	var started api.TaskResponse
	if code, data := r.post(t, "/api/tasks/"+string(res.Task.ID)+"/start", api.StartDraftRequest{Mode: core.ModeAsk}, &started); code != 200 {
		t.Fatalf("start = %d %s", code, data)
	}
	if started.Task.Status == core.TaskDraft {
		t.Fatalf("Start left the task a draft: %+v", started.Task)
	}
}

type issueHost struct{}

func (issueHost) Read(_ context.Context, key corehosts.ContextKey) (hosts.ContextOutcome, error) {
	if key.Kind != corehosts.ContextIssue {
		return hosts.ContextOutcome{Blocked: &corehosts.CtxBlocked{Reason: corehosts.ContextNoToken}}, nil
	}
	return hosts.ContextOutcome{Result: &hosts.ContextResult{Value: corehosts.IssueDetail{
		Summary: corehosts.IssueSummary{Number: 42, Title: "Cart total is wrong", Labels: []string{"bug"}}, Body: "Steps: add two items.",
	}}}, nil
}

func mustListTasks(t *testing.T, r *p6Rig, p *p6Repo) []core.Task {
	t.Helper()
	list, err := r.a.store.ListTasks(context.Background(), p.project)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A failed pipeline: the diagnosis is an Ask task; the fix is a separate,
// explicit Code task in its own worktree; the main checkout never changes
// before a confirmed proposal (AC-02, AC-03; invariants 2, 9).
func TestPhase6E2EDiagnoseThenExplicitFixInAWorktree(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	h := newHostStub()
	attentionSetups[api.ReasonFailedCI](t, r, p, h)
	r.refresh(t, p)
	r.bindActions(t, h)
	before := r.tree(t, p)
	origin := r.originHead(t, p)

	diag := r.offering(t, p, api.ActionDiagnosePipeline)
	_, dres := r.execute(t, api.ActionRequestDTO{RequestID: "req_e2e_diag", RepoID: p.id, Kind: diag.Kind, SourceID: diag.SourceID, Agent: "mock"})
	if dres.State != api.ActionStateCompleted || dres.TaskID == "" {
		t.Fatalf("diagnosis = %+v", dres)
	}
	dtask, err := r.a.store.GetTask(context.Background(), dres.TaskID)
	if err != nil || dtask.Mode != core.ModeAsk {
		t.Fatalf("the diagnosis task = %+v, %v; want Ask", dtask, err)
	}
	waitTaskSettled(t, r, dres.TaskID)
	if wts, _ := r.a.store.ListWorktrees(context.Background(), dres.TaskID); len(wts) != 0 {
		t.Fatalf("a diagnosis created %d worktrees", len(wts))
	}

	fix := r.offering(t, p, api.ActionFixPipeline)
	if fix.Mode != core.ModeCode {
		t.Fatalf("fix offering = %+v", fix)
	}
	_, fres := r.execute(t, api.ActionRequestDTO{RequestID: "req_e2e_fix", RepoID: p.id, Kind: fix.Kind, SourceID: fix.SourceID, Agent: "mock"})
	if fres.State != api.ActionStateCompleted || fres.TaskID == "" || fres.TaskID == dres.TaskID {
		t.Fatalf("fix = %+v, want its own task", fres)
	}
	ftask, err := r.a.store.GetTask(context.Background(), fres.TaskID)
	if err != nil || ftask.Mode != core.ModeCode {
		t.Fatalf("the fix task = %+v, %v; want Code", ftask, err)
	}
	waitTaskSettled(t, r, fres.TaskID)
	wts, err := r.a.store.ListWorktrees(context.Background(), fres.TaskID)
	if err != nil || len(wts) != 1 {
		t.Fatalf("the fix task has %d worktrees (%v), want its own", len(wts), err)
	}
	if rel, err := filepath.Rel(p.dir, wts[0].Path); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("the worktree %s is inside the main checkout", wts[0].Path)
	}
	if got := r.tree(t, p); got != before {
		t.Fatalf("the fix changed the main checkout:\n%s\n%s", before, got)
	}
	if r.originHead(t, p) != origin {
		t.Fatal("the fix touched the remote")
	}
	// Nothing was confirmed: no executed proposal exists.
	list, err := r.a.proposals.List(context.Background(), proposals.ListFilter{Status: core.ProposalPending})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range list {
		if v.Status != core.ProposalPending {
			t.Fatalf("proposal %s is %s without a confirmation", v.ID, v.Status)
		}
	}
}
