package proposals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/mcp/mcptest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// agentProposer is what cmd/ygg wires: the MCP shell creates proposals through
// the real service with the agent as their source, and can do nothing else.
type agentProposer struct {
	svc *Service
	mu  sync.Mutex
	ids []core.ProposalID
}

func (p *agentProposer) Create(ctx context.Context, task core.TaskID, a coreprop.Action, summary string) (mcp.ProposalRef, error) {
	v, err := p.svc.Create(ctx, CreateRequest{Task: task, Action: a, Summary: summary, Source: coreprop.AgentSource{Task: task}})
	if err != nil {
		return mcp.ProposalRef{}, err
	}
	p.mu.Lock()
	p.ids = append(p.ids, v.ID)
	p.mu.Unlock()
	return mcp.ProposalRef{ID: v.ID, Status: string(v.Status), Lines: v.Lines}, nil
}

// askCommitWorld is an Ask task on a real temp git repo with no worktree, an
// MCP server in front of the real proposals service.
type askCommitWorld struct {
	*realWorld
	prop  *agentProposer
	http  *httptest.Server
	token string
	audit *mcptest.FakeAudit
}

func newAskCommitWorld(t *testing.T) *askCommitWorld {
	t.Helper()
	w := newRealWorld(t)
	ws := mcptest.NewFakeWorkspace()
	ws.Set(w.tid, mcp.Snapshot{Project: "p1", Mode: core.ModeAsk, Repos: []mcp.RepoView{
		{ID: w.res.repo.ID, Name: "shop-api", DefaultBranch: "main", InScope: true}, // no worktree
	}})
	a := &askCommitWorld{realWorld: w, prop: &agentProposer{svc: w.svc}, audit: &mcptest.FakeAudit{}}
	srv, err := mcp.New(mcp.Config{
		Workspace: ws, Reader: &mcptest.FakeReader{}, Builds: &mcptest.FakeBuilds{}, Git: &mcptest.FakeGit{},
		Hosts: &mcptest.FakeHosts{}, DevServers: mcp.NoDevServers{}, Proposer: a.prop, Audit: a.audit,
		Redactor: safety.NewRedactor(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	a.http = httptest.NewServer(srv.Handler())
	t.Cleanup(a.http.Close)
	if a.token, err = srv.Issue(w.tid, 1); err != nil {
		t.Fatal(err)
	}
	return a
}

// call runs one tool and returns its text and whether it was an error.
func (a *askCommitWorld) call(name string, args map[string]any) (string, bool) {
	a.t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
	req, _ := http.NewRequest(http.MethodPost, a.http.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		a.t.Fatalf("decode %s: %v", raw, err)
	}
	if out.Error != nil {
		return out.Error.Message, true
	}
	var b strings.Builder
	for _, c := range out.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String(), out.Result.IsError
}

func (a *askCommitWorld) stored(id core.ProposalID) coreprop.Proposal {
	a.t.Helper()
	rec, err := a.svc.cfg.Store.GetProposalRecord(a.ctx, id)
	if err != nil {
		a.t.Fatal(err)
	}
	return rec.Proposal
}

// AC-03, MCP-05, invariants 2 and 9: an Ask task with a dirty main checkout and
// no worktree can propose a commit. The call creates a pending proposal pinned
// to the head and the diff hash and changes nothing on disk; only the
// confirmation commits, and it refuses when the files changed since.
func TestAskTaskProposesCommitOfDirtyMainCheckout(t *testing.T) {
	t.Parallel()
	a := newAskCommitWorld(t)
	dir := a.h.Dir()
	a.h.WriteFile("src/a.txt", "first\n")
	head0, err := a.g.Head(a.ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := a.g.DiffHash(a.ctx, dir, []string{"src/a.txt"})
	if err != nil {
		t.Fatal(err)
	}

	text, isErr := a.call("propose_commit", map[string]any{"repo": "shop-api", "paths": []string{"src/a.txt"}, "message": "feat: add a"})
	if isErr || !strings.Contains(text, "pending") {
		t.Fatalf("propose_commit: error=%v %s", isErr, text)
	}
	if len(a.prop.ids) != 1 {
		t.Fatalf("proposals created: %v", a.prop.ids)
	}
	id := a.prop.ids[0]
	p := a.stored(id)
	c, ok := p.Action.(coreprop.Commit)
	if !ok || p.State.Status() != core.ProposalPending {
		t.Fatalf("proposal %T %s", p.Action, p.State.Status())
	}
	if c.Where != (coreprop.MainCheckout{Repo: a.res.repo.ID}) || len(c.Paths) != 1 || c.Paths[0] != "src/a.txt" {
		t.Fatalf("target and paths: %+v", c)
	}
	if c.ExpectHead != head0 || c.ExpectDiffHash == "" || c.ExpectDiffHash != before {
		t.Fatalf("pins: head %q (want %q), diff %q (want %q)", c.ExpectHead, head0, c.ExpectDiffHash, before)
	}
	// Nothing was committed or staged by the call.
	if head, _ := a.g.Head(a.ctx, dir); head != head0 {
		t.Fatalf("HEAD moved from %s to %s", head0, head)
	}
	if now, _ := a.g.DiffHash(a.ctx, dir, []string{"src/a.txt"}); now != before {
		t.Fatal("the call changed the file")
	}

	// The file changes before the user confirms: the executor refuses.
	a.h.WriteFile("src/a.txt", "changed behind the proposal\n")
	v, err := a.svc.Get(a.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	got := a.confirmAndWait(v, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailStale) {
		t.Fatalf("fail kind %q (%s)", got.FailKind, got.Message)
	}
	if head, _ := a.g.Head(a.ctx, dir); head != head0 {
		t.Fatalf("a stale proposal committed: HEAD %s", head)
	}

	// A proposal made after the change, confirmed with nothing in between, commits.
	if _, isErr := a.call("propose_commit", map[string]any{"repo": "shop-api", "paths": []string{"src/a.txt"}, "message": "feat: add a"}); isErr {
		t.Fatal("second propose_commit refused")
	}
	v2, err := a.svc.Get(a.ctx, a.prop.ids[1])
	if err != nil {
		t.Fatal(err)
	}
	done := a.confirmAndWait(v2, core.ProposalExecuted)
	info, err := a.g.HeadInfo(a.ctx, dir)
	if err != nil || info.ID != done.Result.CommitID || info.Parent != head0 {
		t.Fatalf("HEAD %+v (%v), result %+v", info, err, done.Result)
	}

	// No token in the proposals, their audit rows or the MCP audit records.
	var seen strings.Builder
	for _, pid := range a.prop.ids {
		fmt.Fprintf(&seen, "%+v\n", a.stored(pid))
		pv, _ := a.svc.Get(a.ctx, pid)
		fmt.Fprintf(&seen, "%+v\n", pv)
	}
	rows, err := a.svc.cfg.Store.ListAudit(a.ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		fmt.Fprintf(&seen, "%s %s %s %s\n", r.Actor, r.Action, r.Subject, r.Detail)
	}
	fmt.Fprintf(&seen, "%+v\n", a.audit.Records)
	if strings.Contains(seen.String(), a.token) {
		t.Fatal("the session token appears in a proposal or an audit record")
	}
	if a.audit.Count() != 2 {
		t.Fatalf("mcp audit records: %d, want one per propose_commit call", a.audit.Count())
	}
}

// Push and merge request still need a worktree in Ask mode; the tool is refused
// before anything is created.
func TestAskTaskCannotProposePushWithoutWorktree(t *testing.T) {
	t.Parallel()
	a := newAskCommitWorld(t)
	text, isErr := a.call("propose_push", map[string]any{"repo": "shop-api", "branch": "main", "remote": "origin"})
	if !isErr || len(a.prop.ids) != 0 {
		t.Fatalf("propose_push without a worktree: error=%v %q, proposals %v", isErr, text, a.prop.ids)
	}
}
