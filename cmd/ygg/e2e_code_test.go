package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// startFullApp is startAppWith that also returns the application, so a test
// can reach the stores and the MCP server.
func startFullApp(t *testing.T, cfg config) (*app, string) {
	t.Helper()
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>ygg</html>")}}
	var logs bytes.Buffer
	a, err := newApp(context.Background(), cfg, assets, &logs)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	if tok := a.srv.LaunchToken(); tok != "" {
		openSession(t, base, tok)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { cancel(); <-done }) })
	return a, base
}

func apiDo(t *testing.T, method, url string, body any, client *http.Client) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// A Code-mode task edits a file in its worktree (CRLF content, a folder with a
// space in its name); the agent proposes a commit through MCP; nothing reaches
// the main checkout until the user confirms through the API with a session;
// the audit log shows the whole story (invariants 2, 8, 9 end to end).
func TestCodeModeEndToEnd_ProposalCommitConfirmedThroughTheAPI(t *testing.T) {
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true}
	a, base := startFullApp(t, cfg)

	const file = "src/order list/orders.txt" // a folder with a space, CRLF content
	env := searchtest.GitEnv(t)
	repoDir := searchtest.InitRepo(t, env, map[string]string{file: "line one\r\nline two\r\n", "README.md": "# shop-api\n"})
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, repoDir, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	project, repoID := core.ProjectID(ids[0]), core.RepoID(ids[1])
	repo, err := a.store.GetRepo(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	mainHead := searchtest.GitOutput(t, env, repoDir, "", "rev-parse", "HEAD")

	// The task and its worktree (a real git worktree, in a folder with a space).
	task := dbtest.Task(t, a.store, core.Task{ProjectID: project, Scope: core.ScopeRepo, RepoIDs: []core.RepoID{repoID}, Mode: core.ModeCode})
	wtDir := filepath.Join(t.TempDir(), "work trees", "cart")
	searchtest.RunGit(t, env, repoDir, "worktree", "add", "-q", "-b", "ygg/cart", wtDir)
	if err := a.store.Commit(context.Background(), func(tx *db.Tx) error {
		_, err := tx.CreateWorktree(context.Background(), core.Worktree{
			ID: "wt_e2e", RepoID: repoID, TaskID: task.ID, Name: "cart", Path: wtDir, Branch: "ygg/cart", CreatedAt: time.Now().UTC(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// The "agent" edits a file in the worktree, keeping CRLF.
	edited := "line one\r\nline two\r\nline three\r\n"
	if err := os.WriteFile(filepath.Join(wtDir, filepath.FromSlash(file)), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	// It proposes a commit through its MCP session.
	token, err := a.mcp.Issue(task.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	rpc := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "propose_commit", "arguments": map[string]any{
			"repo": repo.Name, "paths": []string{file}, "message": "feat: a third line", "summary": "Add a third line",
		}},
	}
	body, _ := json.Marshal(rpc)
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	plain := &http.Client{} // an agent has no UI session
	resp, err := plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), `"isError":true`) {
		t.Fatalf("propose_commit = %d %s", resp.StatusCode, raw)
	}

	// The proposal is pending; the main checkout and the worktree HEAD are untouched.
	var list api.ProposalsResponse
	if code := getJSON(t, base+"/api/proposals?task="+string(task.ID), &list); code != 200 || len(list.Proposals) != 1 {
		t.Fatalf("proposals = %d %+v", code, list)
	}
	p := list.Proposals[0]
	if p.Status != core.ProposalPending || !p.FromAgent || p.ActionHash == "" || len(p.Lines) == 0 {
		t.Fatalf("proposal = %+v", p)
	}
	if got := searchtest.GitOutput(t, env, repoDir, "", "rev-parse", "HEAD"); got != mainHead {
		t.Fatal("the main checkout moved before any confirmation")
	}
	if st := searchtest.GitOutput(t, env, repoDir, "", "status", "--porcelain"); st != "" {
		t.Fatalf("the main checkout is dirty before confirmation: %q", st)
	}
	if got := searchtest.GitOutput(t, env, wtDir, "", "rev-parse", "HEAD"); got != mainHead {
		t.Fatal("the worktree committed before any confirmation")
	}

	confirmURL := base + "/api/proposals/" + string(p.ID) + "/confirm"
	// The agent's token cannot confirm; neither can a client without the UI session.
	for name, c := range map[string]*http.Client{"no session": plain} {
		if code, _ := apiDo(t, http.MethodPost, confirmURL, api.ConfirmProposalRequest{Hash: p.ActionHash}, c); code < 400 {
			t.Errorf("confirm with %s = %d", name, code)
		}
	}
	if code, _ := apiDo(t, http.MethodPost, base+"/mcp/proposals/"+string(p.ID)+"/confirm", nil, plain); code < 400 {
		t.Errorf("confirm over the MCP mount = %d", code)
	}
	// A confirm with another hash changes nothing.
	if code, _ := apiDo(t, http.MethodPost, confirmURL, api.ConfirmProposalRequest{Hash: strings.Repeat("0", len(p.ActionHash))}, appClient); code < 400 {
		t.Errorf("confirm with a wrong hash = %d", code)
	}
	if got := searchtest.GitOutput(t, env, wtDir, "", "rev-parse", "HEAD"); got != mainHead {
		t.Fatal("a refused confirmation committed")
	}

	// The user confirms in the UI.
	if code, data := apiDo(t, http.MethodPost, confirmURL, api.ConfirmProposalRequest{Hash: p.ActionHash}, appClient); code >= 300 {
		t.Fatalf("confirm = %d %s", code, data)
	}
	var done api.ProposalDTO
	deadline := time.Now().Add(20 * time.Second)
	for {
		getJSON(t, base+"/api/proposals/"+string(p.ID), &done)
		if done.Status == core.ProposalExecuted {
			break
		}
		if done.Status == core.ProposalFailed || time.Now().After(deadline) {
			t.Fatalf("proposal ended as %q: %+v", done.Status, done)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// The commit is in the worktree branch only; the main checkout is unchanged.
	wtHead := searchtest.GitOutput(t, env, wtDir, "", "rev-parse", "HEAD")
	if wtHead == mainHead || done.Result == nil || !strings.HasPrefix(wtHead, done.Result.CommitID) && !strings.HasPrefix(done.Result.CommitID, wtHead) {
		t.Errorf("worktree HEAD %s, result %+v", wtHead, done.Result)
	}
	if subj := searchtest.GitOutput(t, env, wtDir, "", "log", "-1", "--format=%s"); subj != "feat: a third line" {
		t.Errorf("commit subject = %q", subj)
	}
	if got := searchtest.GitOutput(t, env, repoDir, "", "rev-parse", "HEAD"); got != mainHead {
		t.Error("the main checkout HEAD moved")
	}
	if st := searchtest.GitOutput(t, env, repoDir, "", "status", "--porcelain"); st != "" {
		t.Errorf("the main checkout is dirty: %q", st)
	}
	if b, err := os.ReadFile(filepath.Join(repo.Path, filepath.FromSlash(file))); err != nil || string(b) != "line one\r\nline two\r\n" {
		t.Errorf("main checkout file = %q, %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(wtDir, filepath.FromSlash(file))); string(b) != edited {
		t.Errorf("CRLF edit was rewritten: %q", b)
	}

	// Audit: created, confirmed and executed, written by the right actors.
	entries, err := a.store.ListAudit(context.Background(), "proposal:"+string(p.ID), 50)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
		if strings.Contains(e.Actor+string(e.Detail), token) {
			t.Error("the MCP token is in the audit log")
		}
	}
	joined := strings.Join(actions, " ")
	for _, want := range []string{"proposal.create", "proposal.confirm", "proposal.execute"} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit actions %v lack %q", actions, want)
		}
	}
}
