package mcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
	"github.com/brendatamaar/yggdrasil/internal/mcp/mcptest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// env is a server with fakes: project p1 with shop-api (in scope, has a task
// worktree), shop-web (in scope, no worktree), shop-admin (visible, out of
// scope). "vault" is a tracked-once repo that is not in the snapshot at all.
type env struct {
	srv    *mcp.Server
	http   *httptest.Server
	ws     *mcptest.FakeWorkspace
	rd     *mcptest.FakeReader
	bl     *mcptest.FakeBuilds
	git    *mcptest.FakeGit
	hosts  *mcptest.FakeHosts
	prop   *mcptest.FakeProposer
	audit  *mcptest.FakeAudit
	redact *safety.SecretRedactor
	token  string
	now    atomic.Int64
	task   core.TaskID
	logs   syncBuf
}

// syncBuf is a log sink safe for concurrent handlers.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const (
	apiRepo   core.RepoID = "r-api"
	webRepo   core.RepoID = "r-web"
	adminRepo core.RepoID = "r-admin"
)

func snapshot(mode core.TaskMode) mcp.Snapshot {
	return mcp.Snapshot{
		Project: "p1", Mode: mode,
		Repos: []mcp.RepoView{
			{ID: apiRepo, Name: "shop-api", DefaultBranch: "main", InScope: true, Worktree: "w-api", WorktreeBranch: "ygg/cart"},
			{ID: webRepo, Name: "shop-web", DefaultBranch: "main", InScope: true},
			{ID: adminRepo, Name: "shop-admin", DefaultBranch: "main"},
		},
	}
}

func newEnv(t testing.TB) *env {
	t.Helper()
	e := &env{
		ws: mcptest.NewFakeWorkspace(), rd: &mcptest.FakeReader{Diffs: map[core.RepoID]string{apiRepo: "diff --git a/x b/x\n+hello\n"}},
		bl:    &mcptest.FakeBuilds{ProjectRepos: []core.RepoID{webRepo, apiRepo, adminRepo}, Logs: map[core.BuildID]string{}, Install: "install"},
		git:   &mcptest.FakeGit{},
		hosts: &mcptest.FakeHosts{Remotes: map[core.RepoID]mcp.RemoteInfo{apiRepo: {GitLab: true, Lines: []string{"MR !4 open"}}}},
		prop:  &mcptest.FakeProposer{}, audit: &mcptest.FakeAudit{}, redact: safety.NewRedactor(), task: "t1",
	}
	e.now.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	e.ws.Set(e.task, snapshot(core.ModeCode))
	srv, err := mcp.New(mcp.Config{
		Workspace: e.ws, Reader: e.rd, Builds: e.bl, Git: e.git, Hosts: e.hosts, DevServers: mcp.NoDevServers{},
		Proposer: e.prop, Audit: e.audit, Redactor: e.redact,
		Logger: slog.New(slog.NewTextHandler(&e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:  func() time.Time { return time.Unix(0, e.now.Load()).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	e.http = httptest.NewServer(srv.Handler())
	t.Cleanup(e.http.Close)
	e.token, err = srv.Issue(e.task, 1)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// withReader replaces the server with one that reads git through r, keeping the
// same sessions and token.
func (e *env) withReader(t *testing.T, r mcp.Reader) {
	t.Helper()
	srv, err := mcp.New(mcp.Config{
		Workspace: e.ws, Reader: r, Builds: e.bl, Git: e.git, Hosts: e.hosts, DevServers: mcp.NoDevServers{},
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

// rpcResp is a JSON-RPC response with the parts the tests read.
type rpcResp struct {
	Status int
	Err    *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Meta map[string]any `json:"_meta"`
	} `json:"result"`
	Raw string
}

func (r rpcResp) text() string {
	var b strings.Builder
	for _, c := range r.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// post sends one JSON-RPC request with the given bearer token.
func (e *env) post(t *testing.T, token, method string, params any) rpcResp {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, e.http.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out rpcResp
	_ = json.Unmarshal(raw, &out)
	out.Status, out.Raw = resp.StatusCode, string(raw)
	return out
}

// callTool calls one tool with the env's own token.
func (e *env) callTool(t *testing.T, name string, args map[string]any) rpcResp {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	return e.post(t, e.token, "tools/call", map[string]any{"name": name, "arguments": args})
}

// advance moves the fake clock.
func (e *env) advance(d time.Duration) { e.now.Add(int64(d)) }
