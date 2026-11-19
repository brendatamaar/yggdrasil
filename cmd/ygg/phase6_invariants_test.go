package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/actions/actionstest"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// Safety invariants 1 to 11 (docs/AGENTS.md section 6) through the Phase 6
// surfaces. The new tests here run through the real wiring; the table at the
// end maps every invariant to the tests that fail if it is broken and checks
// that they exist.

func (r *p6Rig) auditDump(t *testing.T) (string, int) {
	t.Helper()
	rows, err := r.a.store.ListAudit(context.Background(), "", 10000)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, "%s %s %s %s\n", row.Actor, row.Action, row.Subject, row.Detail)
	}
	return b.String(), len(rows)
}

// Invariant 1: no force push, no reset --hard, no branch deletion, no deletion
// of files. A pull of a diverged branch is a fast-forward-only proposal: it
// fails and leaves the checkout, the local commit and the remote exactly as
// they were; no tool of the MCP surface can force, reset or delete a branch.
func TestInvariant_1_Phase6PullNeverMergesResetsOrForcePushes(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.remoteCommit(t, p)
	r.localCommit(t, p, "local.txt")
	r.refresh(t, p)
	head, origin, tree := r.head(t, p), r.originHead(t, p), r.tree(t, p)
	branches := searchtest.GitOutput(t, r.env, p.dir, "", "branch", "--list")

	o := r.offering(t, p, api.ActionPullAndCheck)
	_, res := r.execute(t, api.ActionRequestDTO{RequestID: "req_inv1_pull", RepoID: p.id, Kind: o.Kind})
	if len(res.ProposalIDs) != 1 {
		t.Fatalf("result = %+v", res)
	}
	r.confirmHTTP(t, res.ProposalIDs[0])
	if got := r.settle(t, res.ProposalIDs[0]); got.Status == core.ProposalExecuted {
		t.Fatalf("a diverged branch was pulled: %+v", got)
	}
	if r.head(t, p) != head || r.tree(t, p) != tree {
		t.Fatalf("the failed pull changed the checkout: %s -> %s", tree, r.tree(t, p))
	}
	if _, err := os.Stat(filepath.Join(p.dir, "local.txt")); err != nil {
		t.Fatalf("the local commit's file is gone: %v", err)
	}
	if r.originHead(t, p) != origin {
		t.Fatal("the remote moved")
	}
	if got := searchtest.GitOutput(t, r.env, p.dir, "", "branch", "--list"); got != branches {
		t.Fatalf("the branch list changed: %q -> %q", branches, got)
	}
	if merges := searchtest.GitOutput(t, r.env, p.dir, "", "log", "--merges", "--oneline"); merges != "" {
		t.Fatalf("a merge commit was made: %q", merges)
	}

	// The tool surface has no way to force, reset or delete a branch.
	task := r.askTask(t, p)
	token, err := r.a.mcp.Issue(task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	req, _ := http.NewRequest(http.MethodPost, r.base+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	names := regexp.MustCompile(`"name":"([a-z_]+)"`).FindAllStringSubmatch(buf.String(), -1)
	if len(names) < 5 {
		t.Fatalf("the tool list is suspiciously short: %s", buf.String())
	}
	for _, m := range names {
		for _, word := range strings.Split(m[1], "_") {
			switch word {
			case "force", "reset", "remove", "prune", "rm", "unlink":
				t.Errorf("tool %q looks destructive", m[1])
			}
		}
		if strings.Contains(m[1], "delete_branch") || strings.Contains(m[1], "branch_delete") {
			t.Errorf("tool %q deletes a branch", m[1])
		}
	}
}

// Invariant 4: no token in any response, event or audit row of the Phase 6
// flows, whatever the repo, its history and its build logs contain.
func TestInvariant_4_Phase6ResponsesAndAuditHoldNoToken(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	h := newHostStub()
	// A token in a tracked file, in the working diff, in a commit subject.
	if err := os.WriteFile(filepath.Join(p.dir, "config.txt"), []byte("api_key="+actionstest.SecretToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	searchtest.RunGit(t, r.env, p.dir, "add", "config.txt")
	searchtest.RunGit(t, r.env, p.dir, "commit", "-m", "rotate key "+actionstest.SecretToken)
	if err := os.WriteFile(filepath.Join(p.dir, "config.txt"), []byte("api_key="+actionstest.SecretToken+"\nmore\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.refresh(t, p)
	r.bindActions(t, h)

	var seen bytes.Buffer
	capture := func(path string) {
		code, data := apiDo(t, http.MethodGet, r.base+path, nil, appClient)
		if code != 200 {
			t.Fatalf("GET %s = %d", path, code)
		}
		seen.Write(data)
	}
	capture("/api/repos/" + string(p.id) + "/actions")
	o := r.offering(t, p, api.ActionProposeCommits)
	req := api.ActionRequestDTO{RequestID: "req_inv4_1", RepoID: p.id, Kind: o.Kind, Agent: "mock"}
	var pv api.ActionPreviewDTO
	if code, data := r.post(t, "/api/actions/preview", req, &pv); code != 200 {
		t.Fatalf("preview = %d %s", code, data)
	} else {
		seen.Write(data)
	}
	if !strings.Contains(pv.ContextText, "config.txt") {
		t.Fatalf("the preview does not show the diff, so the check proves nothing: %.200q", pv.ContextText)
	}
	var res api.ActionResultDTO
	code, data := r.post(t, "/api/actions/execute", api.ActionExecuteRequest{ActionRequestDTO: req, Digest: pv.Digest, ExpectedHead: pv.ExpectedHead}, &res)
	if code != 200 {
		t.Fatalf("execute = %d %s", code, data)
	}
	seen.Write(data)
	waitTaskSettled(t, r, res.TaskID)
	capture("/api/actions/" + string(req.RequestID))
	from, to := time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	capture("/api/projects/" + string(p.project) + "/standup?from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to))
	capture("/api/notifications")
	capture("/api/tasks/" + string(res.TaskID))

	if strings.Contains(seen.String(), actionstest.SecretToken) {
		t.Fatalf("a response holds the token: %s", seen.String())
	}
	audit, _ := r.auditDump(t)
	if strings.Contains(audit, actionstest.SecretToken) || strings.Contains(audit, "ghp_") {
		t.Fatalf("the audit log holds a token:\n%s", audit)
	}
	// The task's stored prompt (the context the agent got) is redacted as well.
	task, err := r.a.store.GetTask(context.Background(), res.TaskID)
	if err != nil || strings.Contains(task.Prompt, actionstest.SecretToken) {
		t.Fatalf("the task prompt holds the token (%v)", err)
	}
}

// Invariant 6: once a repo is untracked nothing of it is reachable through any
// Phase 6 route, any earlier preview, or the agent tools: only tracked repos
// are visible.
func TestInvariant_6_Phase6UntrackedRepoIsUnreachable(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	if err := os.WriteFile(filepath.Join(p.dir, "notes.txt"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.refresh(t, p)
	r.bindActions(t, newHostStub())
	o := r.offering(t, p, api.ActionProposeCommits)
	req := api.ActionRequestDTO{RequestID: "req_inv6_1", RepoID: p.id, Kind: o.Kind, Agent: "mock"}
	var pv api.ActionPreviewDTO
	if code, data := r.post(t, "/api/actions/preview", req, &pv); code != 200 {
		t.Fatalf("preview = %d %s", code, data)
	}
	task := r.askTask(t, p)
	tasksBefore, proposalsBefore := len(mustListTasks(t, r, p)), pendingCount(t, r)
	repo, err := r.a.store.GetRepo(context.Background(), p.id)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.UntrackRepo(t, r.a.store, repo)
	r.refresh(t)

	id := string(p.id)
	for _, path := range []string{"/actions", "/noise", "/issues", "/issues/1", "/mrs/1/context", "/ci/runs/1/failed-jobs"} {
		if code := r.get(t, "/api/repos/"+id+path, nil); code != http.StatusNotFound {
			t.Errorf("GET /api/repos/<untracked>%s = %d, want 404", path, code)
		}
	}
	if code, _ := r.post(t, "/api/actions/preview", api.ActionRequestDTO{RequestID: "req_inv6_2", RepoID: p.id, Kind: o.Kind, Agent: "mock"}, nil); code != http.StatusNotFound && code != http.StatusConflict {
		t.Errorf("preview of an untracked repo = %d, want a refusal (404 or 409)", code)
	}
	var res api.ActionResultDTO
	code, _ := r.post(t, "/api/actions/execute", api.ActionExecuteRequest{ActionRequestDTO: req, Digest: pv.Digest, ExpectedHead: pv.ExpectedHead}, &res)
	if code == 200 && res.State == api.ActionStateCompleted {
		t.Errorf("an earlier preview executed on an untracked repo: %+v", res)
	}
	if code, _ := r.post(t, "/api/repos/"+id+"/noise/proposals", api.CreateIgnoreProposalRequest{RequestID: "req_inv6_3", Entries: []string{"/x/"}}, nil); code == 200 {
		t.Error("an ignore proposal was made for an untracked repo")
	}
	if raw, isErr := r.mcpCall(t, task.ID, "propose_commit", map[string]any{"repo": id, "paths": []string{"notes.txt"}, "message": "m", "summary": "s"}); !isErr {
		t.Errorf("the agent proposed a commit for an untracked repo: %s", raw)
	}
	if raw, isErr := r.mcpCall(t, task.ID, "repo_status", map[string]any{"repo": id}); !isErr {
		t.Errorf("the agent read an untracked repo: %s", raw)
	}
	if got := len(mustListTasks(t, r, p)); got != tasksBefore {
		t.Errorf("%d tasks, was %d: a task was made for an untracked repo", got, tasksBefore)
	}
	if got := pendingCount(t, r); got != proposalsBefore {
		t.Errorf("%d pending proposals, was %d", got, proposalsBefore)
	}
}

func pendingCount(t *testing.T, r *p6Rig) int {
	t.Helper()
	list, err := r.a.proposals.List(context.Background(), proposals.ListFilter{Status: core.ProposalPending})
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

// Invariant 8: every state change of a Phase 6 surface writes an audit row, and
// a read or a preview writes none.
func TestInvariant_8_Phase6StateChangesAreAudited(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	h := newHostStub()
	st := h.state(p.id)
	st.linked, st.token = true, true
	if err := os.WriteFile(filepath.Join(p.dir, "notes.txt"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.refresh(t, p)
	r.bindActionsWith(t, h, issueHost{})
	_, base := r.auditDump(t)

	// Reads and a preview: no audit row.
	o := r.offering(t, p, api.ActionProposeCommits)
	req := api.ActionRequestDTO{RequestID: "req_inv8_1", RepoID: p.id, Kind: o.Kind, Agent: "mock"}
	var pv api.ActionPreviewDTO
	r.post(t, "/api/actions/preview", req, &pv)
	r.get(t, "/api/repos/"+string(p.id)+"/noise", nil)
	r.get(t, "/api/notifications", nil)
	if _, n := r.auditDump(t); n != base {
		t.Fatalf("reads and a preview wrote %d audit rows", n-base)
	}

	// Each mutation writes at least one row.
	mutations := map[string]func(){
		"action execute": func() {
			r.post(t, "/api/actions/execute", api.ActionExecuteRequest{ActionRequestDTO: req, Digest: pv.Digest, ExpectedHead: pv.ExpectedHead}, nil)
		},
		"issue import": func() {
			r.post(t, "/api/repos/"+string(p.id)+"/issues/42/draft", api.ImportIssueDraftRequest{RequestID: "req_inv8_2", Agent: "mock"}, nil)
		},
		"pull action": func() {
			r.remoteCommit(t, p)
			r.refresh(t, p)
			pull := r.offering(t, p, api.ActionPullAndCheck)
			r.execute(t, api.ActionRequestDTO{RequestID: "req_inv8_3", RepoID: p.id, Kind: pull.Kind})
		},
	}
	for name, run := range mutations {
		_, before := r.auditDump(t)
		run()
		if _, after := r.auditDump(t); after <= before {
			t.Errorf("%s wrote no audit row", name)
		}
	}
	waitTaskSettled(t, r, mustListTasks(t, r, p)[0].ID)
}

// ---- the table ----

// phase6Invariants maps each safety invariant (docs/AGENTS.md section 6) to the
// tests through the Phase 6 surfaces that fail if it is broken. TestPhase6
// InvariantTableNamesExist fails when a named test is renamed or removed.
var phase6Invariants = map[int][]string{
	1:  {"TestInvariant_1_Phase6PullNeverMergesResetsOrForcePushes", "TestPhase6PullActionCreatesAPendingProposalOnly"},
	2:  {"TestInvariant_2_Phase6NothingMutatesWithoutConfirmation", "TestInvariant_2_phase6_mutations_never_decide_a_proposal", "TestInvariant_2_NoEffectWithoutCommittedIntent", "TestInvariant_2_NewRepoNothingMutatesWithoutConfirmation", "TestPhase6E2ECommitProposalsFromAnAskTask"},
	3:  {"TestInvariant_3_PublishNoTokenChild", "TestInvariant_3_NewRepoHelpersStartCleanChildren"},
	4:  {"TestInvariant_4_Phase6ResponsesAndAuditHoldNoToken", "TestInvariant_4_ActionsHoldNoToken", "TestInvariant_4_Phase6PathsNeverShowATokenInAuditViewOrEvents", "TestInvariant_4_HostContextRedacted", "TestInvariant_4_StandupTextHoldsNoToken", "TestInvariant_4_NewRepoNoTokenInAuditLogOrRows", "TestInvariant_4_PublishCredentialFree"},
	5:  {"TestInvariant_5_StalePinRefusedPerKind", "TestPhase6E2ECommitProposalsFromAnAskTask", "TestPhase6E2EIgnoreEditKeepsCRLFAndRefusesAStaleFile"},
	6:  {"TestInvariant_6_Phase6UntrackedRepoIsUnreachable", "TestInvariant_6_ImportRefusesRepoOutsideTheProjectWithoutCollateralWrites", "TestScopeReaderContractRealAdapter"},
	7:  {"TestPhase6RoutesRefuseForeignHostOriginAndMissingSession", "TestInvariant_7_mcp_host_and_origin"},
	8:  {"TestInvariant_8_Phase6StateChangesAreAudited", "TestInvariant_8_ImportAndSubmissionFaultsRollBackAllWrites", "TestInvariant_8_every_state_change_has_an_audit_command"},
	9:  {"TestPhase6NeedsAttentionPrimaryActionsRun", "TestPhase6NeedsAttentionActions", "TestInvariant_9_AskDraftStartIsIntegrityChecked"},
	10: {"TestInvariant_10_UnreadableRepoNeverRunsAnything", "TestInvariant_10_UntrustedRepoDraftOnlyStartsInAsk", "TestPhase6UnreadableRepoActionEndsInAWaitingDraft"},
	11: {"TestPropChildNeverGetsMoreTools", "TestPhase6E2EDiagnoseThenExplicitFixInAWorktree"},
}

func TestPhase6InvariantTableNamesExist(t *testing.T) {
	root := filepath.Join("..", "..")
	defined := map[string]bool{}
	re := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "web") {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range re.FindAllSubmatch(data, -1) {
			defined[string(m[1])] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 11; n++ {
		names := phase6Invariants[n]
		if len(names) == 0 {
			t.Errorf("invariant %d has no test in the table", n)
		}
		for _, name := range names {
			if !defined[name] {
				t.Errorf("invariant %d: the test %s does not exist", n, name)
			}
		}
	}
	if len(phase6Invariants) != 11 {
		t.Errorf("the table has %d invariants, want 11", len(phase6Invariants))
	}
}
