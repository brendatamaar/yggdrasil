package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
	tt "github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"

	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
)

// ---- dev servers ----

type fakeDev struct {
	mu    sync.Mutex
	views []devservers.View
	err   error
	calls []string
	logs  []devservers.LogLine
}

func (f *fakeDev) rec(op string, t devservers.Target, c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s %s/%s/%s", op, t.Repo, t.Worktree, c))
}

func (f *fakeDev) view(t devservers.Target, c string, status string) devservers.View {
	k := dsc.Key{Repo: t.Repo, Worktree: t.Worktree, CommandKey: c}
	return devservers.View{ID: devservers.IDOf(k), Key: k, Status: status,
		Block: core.PortBlock{Base: 20000, Size: 20}, Ports: []dsc.Port{20001, 3000}, Announced: []dsc.Port{20001}, Warnings: []dsc.Port{3000},
		Since: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeDev) Start(_ context.Context, t devservers.Target, c string) (devservers.View, error) {
	f.rec("start", t, c)
	return f.view(t, c, "starting"), f.err
}

func (f *fakeDev) Stop(_ context.Context, t devservers.Target, c string) (devservers.View, error) {
	f.rec("stop", t, c)
	return f.view(t, c, "stopping"), f.err
}

func (f *fakeDev) Restart(_ context.Context, t devservers.Target, c string) (devservers.View, error) {
	f.rec("restart", t, c)
	return f.view(t, c, "stopping"), f.err
}

func (f *fakeDev) ByID(_ context.Context, id string) (devservers.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.views {
		if v.ID == id {
			return v, nil
		}
	}
	return devservers.View{}, apperr.New(apperr.NotFound, "That dev server doesn't exist (any more).", nil)
}

func (f *fakeDev) Logs(t devservers.Target, c string, n int) ([]devservers.LogLine, error) {
	f.rec(fmt.Sprintf("logs%d", n), t, c)
	return f.logs, f.err
}

func (f *fakeDev) Servers(context.Context) ([]devservers.View, error) { return f.views, nil }
func (f *fakeDev) RepoName(context.Context, core.RepoID) string       { return "shop-api" }

func devEnv(t *testing.T, f *fakeDev) *env {
	return newEnv(t, func(d *Deps) { d.Phase5 = Phase5Services{DevServers: f} })
}

func TestDevServersListMapsViews(t *testing.T) {
	f := &fakeDev{}
	f.views = []devservers.View{f.view(devservers.Target{Repo: "repo_a", Worktree: "wt_1"}, "dev", "running")}
	e := devEnv(t, f)
	resp, data := e.do(t, "GET", "/api/devservers", nil)
	var out DevServersResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || len(out.Servers) != 1 {
		t.Fatalf("list = %d %s", resp.StatusCode, data)
	}
	s := out.Servers[0]
	if s.RepoName != "shop-api" || s.Status != DevServerRunning || s.BlockBase != 20000 || s.Command != "dev" || s.Since == nil {
		t.Fatalf("dto = %+v", s)
	}
	if len(s.Ports) != 2 || !s.Ports[0].Announced || s.Ports[0].OutsideBlock || s.Ports[1].Announced || !s.Ports[1].OutsideBlock {
		t.Fatalf("ports = %+v", s.Ports)
	}
	if strings.Contains(string(data), "/") && strings.Contains(string(data), "dir") {
		t.Fatalf("the reply mentions a folder: %s", data)
	}
}

func TestDevServerActionsFindServerByID(t *testing.T) {
	f := &fakeDev{}
	v := f.view(devservers.Target{Repo: "repo_a", Worktree: "wt_1"}, "dev", "running")
	f.views = []devservers.View{v}
	e := devEnv(t, f)
	for _, op := range []string{"start", "stop", "restart"} {
		resp, data := e.do(t, "POST", "/api/devservers/"+v.ID+"/"+op, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("%s = %d %s", op, resp.StatusCode, data)
		}
	}
	want := []string{"start repo_a/wt_1/dev", "stop repo_a/wt_1/dev", "restart repo_a/wt_1/dev"}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestDevServerStartWithBodyChecksTheID(t *testing.T) {
	f := &fakeDev{}
	e := devEnv(t, f)
	k := dsc.Key{Repo: "repo_a", CommandKey: "dev"}
	id := devservers.IDOf(k)
	body := DevServerActionRequest{RepoID: "repo_a", Command: "dev"}
	if resp, data := e.do(t, "POST", "/api/devservers/"+id+"/start", body); resp.StatusCode != 200 {
		t.Fatalf("start = %d %s", resp.StatusCode, data)
	}
	// An ID that belongs to another server is refused and nothing starts.
	other := devservers.IDOf(dsc.Key{Repo: "repo_b", CommandKey: "dev"})
	n := len(f.calls)
	if resp, _ := e.do(t, "POST", "/api/devservers/"+other+"/start", body); resp.StatusCode != 400 {
		t.Fatalf("mismatch = %d, want 400", resp.StatusCode)
	}
	// A path-like command or repo is refused by the boundary parser.
	for _, b := range []DevServerActionRequest{{RepoID: "repo_a", Command: "../x"}, {RepoID: "C:\\x", Command: "dev"}, {RepoID: "repo_a", Command: ""}} {
		if resp, _ := e.do(t, "POST", "/api/devservers/"+id+"/start", b); resp.StatusCode != 400 {
			t.Fatalf("%+v = %d, want 400", b, resp.StatusCode)
		}
	}
	if len(f.calls) != n {
		t.Fatalf("the service was called for refused requests: %v", f.calls)
	}
	// Unknown fields (such as a folder) are refused.
	raw := map[string]string{"repoId": "repo_a", "command": "dev", "dir": "/etc"}
	if resp, _ := e.do(t, "POST", "/api/devservers/"+id+"/start", raw); resp.StatusCode != 400 {
		t.Fatalf("dir field = %d, want 400", resp.StatusCode)
	}
}

func TestDevServerErrorClasses(t *testing.T) {
	f := &fakeDev{}
	e := devEnv(t, f)
	id := devservers.IDOf(dsc.Key{Repo: "repo_a", CommandKey: "dev"})
	body := DevServerActionRequest{RepoID: "repo_a", Command: "dev"}
	for _, c := range []struct {
		class  apperr.Class
		status int
		code   ErrorCode
	}{
		{apperr.NotFound, 404, ErrNotFound}, {apperr.Conflict, 409, ErrConflict},
		{apperr.Invalid, 400, ErrBadRequest}, {apperr.Unavailable, 503, ErrUnavailable},
	} {
		f.err = apperr.New(c.class, "Plain message for the person.", errors.New("internal detail /secret/path"))
		resp, data := e.do(t, "POST", "/api/devservers/"+id+"/start", body)
		var b ErrorBody
		if resp.StatusCode != c.status || json.Unmarshal(data, &b) != nil || b.Code != c.code || b.Message != "Plain message for the person." {
			t.Errorf("%v: %d %s", c.class, resp.StatusCode, data)
		}
		if strings.Contains(string(data), "secret") {
			t.Errorf("internal error text leaked: %s", data)
		}
	}
	f.err = errors.New("boom")
	if resp, _ := e.do(t, "POST", "/api/devservers/"+id+"/start", body); resp.StatusCode != 500 {
		t.Errorf("unclassified = %d, want 500", resp.StatusCode)
	}
}

func TestDevServerUnknownAndMalformedIDs(t *testing.T) {
	f := &fakeDev{}
	e := devEnv(t, f)
	for _, p := range []string{"/api/devservers/dsv_0000/stop", "/api/devservers/dsv_0000/log", "/api/devservers/..%2Fx/stop"} {
		method := "POST"
		if strings.HasSuffix(p, "/log") {
			method = "GET"
		}
		if resp, _ := e.do(t, method, p, nil); resp.StatusCode != 404 {
			t.Errorf("%s = %d, want 404", p, resp.StatusCode)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestDevServerLogTail(t *testing.T) {
	f := &fakeDev{logs: []devservers.LogLine{{Seq: 7, Stream: devservers.StreamStdout, Line: "a"}, {Seq: 8, Stream: devservers.StreamStderr, Line: "b"}}}
	v := f.view(devservers.Target{Repo: "repo_a"}, "dev", "running")
	f.views = []devservers.View{v}
	e := devEnv(t, f)
	resp, data := e.do(t, "GET", "/api/devservers/"+v.ID+"/log?tail=50", nil)
	var out DevServerLogResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || len(out.Lines) != 2 || out.Lines[1].Line != "b" || out.Lines[1].Seq != 8 || out.Lines[1].Stream != "stderr" || out.Lines[0].Seq != 7 || string(out.Lines[0].ID) != v.ID {
		t.Fatalf("log = %d %s", resp.StatusCode, data)
	}
	if f.calls[len(f.calls)-1] != "logs50 repo_a//dev" {
		t.Fatalf("calls = %v", f.calls)
	}
	for _, q := range []string{"tail=0", "tail=-1", "tail=x", "tail=100000"} {
		if resp, _ := e.do(t, "GET", "/api/devservers/"+v.ID+"/log?"+q, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", q, resp.StatusCode)
		}
	}
	// Without tail, a default applies.
	e.do(t, "GET", "/api/devservers/"+v.ID+"/log", nil)
	if f.calls[len(f.calls)-1] != "logs200 repo_a//dev" {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestDevServerPublishes(t *testing.T) {
	f := &fakeDev{}
	e := devEnv(t, f)
	c := e.dial(t)
	v := f.view(devservers.Target{Repo: "repo_a"}, "dev", "running")
	subscribe(t, c, TopicDevServers)
	subscribe(t, c, TopicDevServerPrefix+v.ID)
	time.Sleep(150 * time.Millisecond) // subscriptions have no ack
	e.srv.PublishServer(v.ID, v)
	e.srv.PublishLog(v.ID, devservers.LogLine{Seq: 1, Stream: devservers.StreamStdout, Line: "ready on :3000"})
	got := map[string]string{}
	for i := 0; i < 3; i++ {
		m, err := readMsg(t, c, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		got[m.Topic] += string(m.Data)
	}
	if !strings.Contains(got[TopicDevServers], `"repoName":"shop-api"`) ||
		!strings.Contains(got[TopicDevServerPrefix+v.ID], "ready on :3000") ||
		!strings.Contains(got[TopicDevServerPrefix+v.ID], `"status":"running"`) {
		t.Fatalf("messages = %v", got)
	}
}

// ---- worktree environment ----

type fakeEnv struct {
	mu    sync.Mutex
	set   []RepoEnvDTO
	err   error
	copy  []core.WorktreeID
	state WorktreeEnvDTO
}

func (f *fakeEnv) RepoEnv(_ context.Context, id core.RepoID) (RepoEnvDTO, error) {
	return RepoEnvDTO{RepoID: id}, f.err
}

func (f *fakeEnv) SetRepoEnv(_ context.Context, _ core.RepoID, in RepoEnvDTO) (RepoEnvDTO, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = append(f.set, in)
	return in, f.err
}

func (f *fakeEnv) WorktreeEnv(_ context.Context, w core.WorktreeID) (WorktreeEnvDTO, error) {
	s := f.state
	s.WorktreeID = w
	return s, f.err
}

func (f *fakeEnv) CopyProfile(_ context.Context, w core.WorktreeID) (WorktreeEnvDTO, error) {
	f.mu.Lock()
	f.copy = append(f.copy, w)
	f.mu.Unlock()
	return WorktreeEnvDTO{WorktreeID: w, Compose: ComposeOff}, f.err
}

func envEnv(t *testing.T, f *fakeEnv) *env {
	return newEnv(t, func(d *Deps) { d.Phase5 = Phase5Services{WorktreeEnv: f} })
}

func TestRepoEnvPutRejectsBadSettings(t *testing.T) {
	f := &fakeEnv{}
	e := envEnv(t, f)
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf("f%d.env", i)
	}
	cases := map[string]RepoEnvDTO{
		"absolute":     {ProfilePaths: []string{"/etc/passwd"}},
		"drive":        {ProfilePaths: []string{`C:\secrets\.env`}},
		"unc":          {ProfilePaths: []string{`\\host\share\x`}},
		"parent":       {ProfilePaths: []string{"../outside/.env"}},
		"inner parent": {ProfilePaths: []string{"a/../../x"}},
		"git":          {ProfilePaths: []string{".git/config"}},
		"nul":          {ProfilePaths: []string{"a\x00b"}},
		"control":      {ProfilePaths: []string{"a\nb"}},
		"glob":         {ProfilePaths: []string{"config/*.env"}},
		"empty entry":  {ProfilePaths: []string{""}},
		"too many":     {ProfilePaths: many},
		"bad compose":  {ComposeFile: "../docker-compose.yml"},
		"no file":      {ComposeEnabled: true},
	}
	for name, body := range cases {
		resp, data := e.do(t, "PUT", "/api/repos/repo_a/env", body)
		var b ErrorBody
		if resp.StatusCode != 400 || json.Unmarshal(data, &b) != nil || b.Code != ErrBadRequest || b.Message == "" {
			t.Errorf("%s: %d %s", name, resp.StatusCode, data)
		}
	}
	if len(f.set) != 0 {
		t.Fatalf("the service stored rejected settings: %+v", f.set)
	}
	if resp, _ := e.do(t, "PUT", "/api/repos/repo_a/env", RepoEnvDTO{RepoID: "repo_b"}); resp.StatusCode != 400 {
		t.Errorf("repo mismatch = %d, want 400", resp.StatusCode)
	}
}

func TestRepoEnvPutNormalisesAndStores(t *testing.T) {
	f := &fakeEnv{}
	e := envEnv(t, f)
	body := RepoEnvDTO{ProfilePaths: []string{`config\.env.local`, "./.env", ".ENV"}, AgentCopy: true, ComposeEnabled: true, ComposeFile: "deploy/compose.yml"}
	resp, data := e.do(t, "PUT", "/api/repos/repo_a/env", body)
	var out RepoEnvDTO
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil {
		t.Fatalf("put = %d %s", resp.StatusCode, data)
	}
	if out.RepoID != "repo_a" || strings.Join(out.ProfilePaths, ",") != "config/.env.local,.env" || !out.AgentCopy || out.ComposeFile != "deploy/compose.yml" {
		t.Fatalf("stored = %+v", out)
	}
	resp, data = e.do(t, "GET", "/api/repos/repo_a/env", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"profilePaths":[]`) {
		t.Fatalf("get = %d %s", resp.StatusCode, data)
	}
}

func TestWorktreeEnvEndpointsAndErrorMapping(t *testing.T) {
	f := &fakeEnv{state: WorktreeEnvDTO{BlockBase: 20020, BlockSize: 20, Compose: ComposeOff}}
	e := envEnv(t, f)
	resp, data := e.do(t, "GET", "/api/worktrees/wt_1/env", nil)
	var out WorktreeEnvDTO
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || out.BlockBase != 20020 || out.WorktreeID != "wt_1" || !strings.Contains(string(data), `"profile":[]`) {
		t.Fatalf("get = %d %s", resp.StatusCode, data)
	}
	if resp, data = e.do(t, "POST", "/api/worktrees/wt_1/profile/copy", nil); resp.StatusCode != 200 || len(f.copy) != 1 || f.copy[0] != "wt_1" {
		t.Fatalf("copy = %d %s %v", resp.StatusCode, data, f.copy)
	}
	for _, c := range []struct {
		err    error
		status int
	}{
		{worktreeenv.ErrNotFound, 404}, {worktreeenv.ErrConflict, 409},
		{&worktreeenv.Error{Msg: "Use a port range", Err: errors.New("x")}, 400},
		{apperr.New(apperr.Unavailable, "later", nil), 503},
		{errors.New("boom"), 500},
	} {
		f.err = c.err
		if resp, _ := e.do(t, "GET", "/api/worktrees/wt_1/env", nil); resp.StatusCode != c.status {
			t.Errorf("%v = %d, want %d", c.err, resp.StatusCode, c.status)
		}
	}
	if resp, _ := e.do(t, "GET", "/api/worktrees/a%2Fb/env", nil); resp.StatusCode != 404 {
		t.Errorf("bad id = %d", resp.StatusCode)
	}
}

func TestComposeEndpointsAreStubs(t *testing.T) {
	e := envEnv(t, &fakeEnv{})
	for _, op := range []string{"confirm", "up", "stop", "down"} {
		resp, data := e.do(t, "POST", "/api/worktrees/wt_1/compose/"+op, nil)
		var b ErrorBody
		if resp.StatusCode != 501 || json.Unmarshal(data, &b) != nil || b.Message == "" {
			t.Errorf("%s = %d %s", op, resp.StatusCode, data)
		}
	}
}

func TestPublishWorktreeEnv(t *testing.T) {
	e := envEnv(t, &fakeEnv{state: WorktreeEnvDTO{BlockBase: 20040, BlockSize: 20, Compose: ComposeOff}})
	c := e.dial(t)
	subscribe(t, c, TopicWorktreeEnv)
	time.Sleep(150 * time.Millisecond) // subscriptions have no ack
	e.srv.PublishWorktreeEnv("wt_9")
	m, err := readMsg(t, c, 3*time.Second)
	if err != nil || m.Topic != TopicWorktreeEnv || !strings.Contains(string(m.Data), `"worktreeId":"wt_9"`) {
		t.Fatalf("message = %+v %v", m, err)
	}
}

func TestPhase5RoutesAnswer501WithoutServices(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/devservers"}, {"POST", "/api/devservers/dsv_1/start"}, {"GET", "/api/devservers/dsv_1/log"},
		{"GET", "/api/repos/r1/env"}, {"PUT", "/api/repos/r1/env"}, {"GET", "/api/worktrees/w1/env"},
		{"POST", "/api/worktrees/w1/profile/copy"}, {"POST", "/api/terminal"}, {"GET", "/ws/terminal?token=x"},
	} {
		resp, data := e.do(t, c.method, c.path, nil)
		var b ErrorBody
		if resp.StatusCode != 501 || json.Unmarshal(data, &b) != nil || b.Code != ErrUnavailable || b.Message == "" {
			t.Errorf("%s %s = %d %s", c.method, c.path, resp.StatusCode, data)
		}
	}
}

// ---- terminal ----

type termEnv struct {
	*env
	svc     *terminal.Service
	starter *tt.Starter
	audit   *tt.MemAudit
	logs    *syncBuf
}

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

func newTermEnv(t *testing.T) *termEnv {
	t.Helper()
	te := &termEnv{starter: &tt.Starter{}, audit: &tt.MemAudit{}, logs: &syncBuf{}}
	folders := tt.NewFakeFolders()
	folders.AddRepo("repo_a", t.TempDir())
	svc, err := terminal.New(terminal.Config{
		Folders: folders, Env: safety.NewEnvBuilder(), Audit: te.audit, Tokens: &tt.CounterTokens{},
		Start: te.starter.Start, Shell: func() []string { return []string{"fake-shell"} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})
	te.svc = svc
	log := slog.New(safety.NewLogHandler(slog.NewTextHandler(te.logs, &slog.HandlerOptions{Level: slog.LevelDebug}), safety.NewRedactor()))
	te.env = newEnv(t, func(d *Deps) { d.Log = log; d.Phase5 = Phase5Services{Terminal: svc} })
	return te
}

func (te *termEnv) token(t *testing.T, body any) string {
	t.Helper()
	resp, data := te.do(t, "POST", "/api/terminal", body)
	var out TerminalTokenResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || out.Token == "" || out.ExpiresIn != 30 {
		t.Fatalf("token = %d %s", resp.StatusCode, data)
	}
	return out.Token
}

func (te *termEnv) dialTerm(token string, mod ...func(*websocket.DialOptions)) (*websocket.Conn, *http.Response, error) {
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": []string{te.cookie.String()}}}
	for _, m := range mod {
		m(opts)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(te.ts.URL, "http")+"/ws/terminal?token="+token, opts)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTerminalSessionOverWebSocket(t *testing.T) {
	te := newTermEnv(t)
	tok := te.token(t, TerminalTokenRequest{RepoID: "repo_a"})
	c, resp, err := te.dialTerm(tok)
	if err != nil {
		body := ""
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			body = resp.Status + " " + string(b)
		}
		t.Fatalf("dial: %v %s", err, body)
	}
	closeBody(resp)
	defer c.CloseNow() //nolint:errcheck // test cleanup
	waitFor(t, "the shell", func() bool { return len(te.starter.PTYs()) == 1 })
	pty := te.starter.PTYs()[0]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "keystrokes", func() bool { return strings.Contains(pty.Written(), "ls\r") })
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resize", func() bool { return len(pty.Resizes()) > 0 })
	pty.Output("hello")
	var got strings.Builder // the fake terminal may echo the keystrokes first
	for !strings.Contains(got.String(), "hello") {
		typ, data, err := c.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
			t.Fatalf("output = %v %q %v", typ, got.String(), err)
		}
		got.Write(data)
	}
	pty.Exit(0)
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("end = %v, want a normal close", err)
	}
}

func TestTerminalTokenRequestRules(t *testing.T) {
	te := newTermEnv(t)
	for name, body := range map[string]any{
		"both":    map[string]string{"repoId": "repo_a", "worktreeId": "wt_1"},
		"neither": map[string]string{},
		"path":    map[string]string{"repoId": "repo_a", "path": "/etc"},
		"slash":   map[string]string{"repoId": "a/b"},
	} {
		if resp, data := te.do(t, "POST", "/api/terminal", body); resp.StatusCode != 400 {
			t.Errorf("%s = %d %s", name, resp.StatusCode, data)
		}
	}
	// An untracked repo is a 404 with a plain message.
	resp, data := te.do(t, "POST", "/api/terminal", TerminalTokenRequest{RepoID: "repo_zz"})
	var b ErrorBody
	if resp.StatusCode != 404 || json.Unmarshal(data, &b) != nil || b.Code != ErrNotFound || b.Message == "" {
		t.Errorf("untracked = %d %s", resp.StatusCode, data)
	}
}

// Invariant 7: a request from another origin or host is refused before the
// token is touched, so it cannot burn a real user's token.
func TestTerminalSocketChecksHostAndOriginBeforeTheToken(t *testing.T) {
	te := newTermEnv(t)
	tok := te.token(t, TerminalTokenRequest{RepoID: "repo_a"})
	for name, mod := range map[string]func(*websocket.DialOptions){
		"foreign origin": func(o *websocket.DialOptions) { o.HTTPHeader.Set("Origin", "https://evil.example") },
		"opaque origin":  func(o *websocket.DialOptions) { o.HTTPHeader.Set("Origin", "null") },
		"foreign host":   func(o *websocket.DialOptions) { o.Host = "evil.example" },
	} {
		c, resp, err := te.dialTerm(tok, mod)
		if err == nil {
			_ = c.CloseNow()
			t.Fatalf("%s: the upgrade succeeded", name)
		}
		if resp == nil || resp.StatusCode != 403 {
			t.Fatalf("%s: response = %v, want 403", name, resp)
		}
		closeBody(resp)
	}
	if len(te.starter.PTYs()) != 0 || te.audit.Calls() != 0 {
		t.Fatal("a rejected request reached the terminal service")
	}
	// The token still works for the real page.
	c, resp, err := te.dialTerm(tok, func(o *websocket.DialOptions) { o.HTTPHeader.Set("Origin", te.ts.URL) })
	if err != nil {
		t.Fatalf("the token was burned by a rejected request: %v", err)
	}
	closeBody(resp)
	_ = c.CloseNow()
}

func TestTerminalSocketNeedsTheSessionAndASingleUseToken(t *testing.T) {
	te := newTermEnv(t)
	tok := te.token(t, TerminalTokenRequest{RepoID: "repo_a"})
	_, resp, err := te.dialTerm(tok, func(o *websocket.DialOptions) { o.HTTPHeader.Del("Cookie") })
	if err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("without a session: %v %v", err, resp)
	}
	closeBody(resp)
	c, resp, err := te.dialTerm(tok)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(resp)
	defer c.CloseNow()                                     //nolint:errcheck // test cleanup
	for _, bad := range []string{tok, "", "not-a-token"} { // reused, empty, malformed
		_, resp, err = te.dialTerm(bad)
		if err == nil || resp == nil || resp.StatusCode != 400 {
			t.Fatalf("token %q: %v %v", bad, err, resp)
		}
		var b ErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&b)
		closeBody(resp)
		if b.Message == "" {
			t.Fatalf("token %q: no plain message", bad)
		}
	}
}

// The raw token is a secret: it must not appear in any log line, whether the
// socket opened, was refused for its origin or reused the token.
func TestTerminalTokenNeverInLogs(t *testing.T) {
	te := newTermEnv(t)
	tok := te.token(t, TerminalTokenRequest{RepoID: "repo_a"})
	_, r1, _ := te.dialTerm(tok, func(o *websocket.DialOptions) { o.HTTPHeader.Set("Origin", "https://evil.example") })
	if r1 != nil {
		closeBody(r1)
	}
	c, r2, err := te.dialTerm(tok)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(r2)
	_, r3, _ := te.dialTerm(tok) // reused
	if r3 != nil {
		closeBody(r3)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "session end", func() bool { return te.audit.Calls() >= 2 })
	te.srv.Close()
	logs := te.logs.String()
	if !strings.Contains(logs, "/ws/terminal") {
		t.Fatalf("the access log recorded nothing for the route:\n%s", logs)
	}
	if strings.Contains(logs, tok) || strings.Contains(logs, "token=") {
		t.Fatalf("the token is in the logs:\n%s", logs)
	}
}

func TestTerminalIsNotOnTheMCPPath(t *testing.T) {
	te := newTermEnv(t)
	for _, p := range []string{"/mcp/terminal", "/mcp/api/terminal"} {
		if resp, _ := te.do(t, "POST", p, TerminalTokenRequest{RepoID: "repo_a"}); resp.StatusCode == 200 {
			t.Errorf("%s answered 200", p)
		}
	}
}

// ---- Socket contract ----

type wsPair struct {
	client *websocket.Conn
	server *wsSocket

	mu     sync.Mutex
	out    bytes.Buffer
	closed bool
	reason string
	done   chan struct{}
}

func TestWebSocketAdapterMeetsTheSocketContract(t *testing.T) {
	tt.SocketContract(t, func() tt.SocketPair {
		accepted := make(chan *wsSocket, 1)
		srv := newHTTPTest(t, accepted)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		closeBody(resp)
		p := &wsPair{client: conn, server: <-accepted, done: make(chan struct{})}
		t.Cleanup(func() { _ = conn.CloseNow() })
		go func() { // the browser: collects output, notes the close
			defer close(p.done)
			for {
				typ, data, err := conn.Read(context.Background())
				p.mu.Lock()
				if err != nil {
					p.closed = true
					if websocket.CloseStatus(err) != -1 {
						p.reason = closeReason(err)
					}
					p.mu.Unlock()
					return
				}
				if typ == websocket.MessageBinary {
					p.out.Write(data)
				}
				p.mu.Unlock()
			}
		}()
		return tt.SocketPair{
			Server: p.server,
			Send: func(kind ct.FrameKind, data []byte) {
				typ := websocket.MessageBinary
				if kind == ct.TextFrame {
					typ = websocket.MessageText
				}
				if err := conn.Write(context.Background(), typ, data); err != nil {
					t.Error(err)
				}
			},
			Disconnect: func() { _ = conn.CloseNow() },
			Output: func() string { // wait until the output stops growing
				last, stable := -1, 0
				for i := 0; i < 100 && stable < 10; i++ {
					time.Sleep(10 * time.Millisecond)
					p.mu.Lock()
					n := p.out.Len()
					p.mu.Unlock()
					if n == last && n > 0 {
						stable++
					} else {
						stable = 0
					}
					last = n
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				return p.out.String()
			},
			Closed: func() (bool, string) {
				select {
				case <-p.done:
				case <-time.After(3 * time.Second):
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				return p.closed, p.reason
			},
		}
	})
}

func closeReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}

// newHTTPTest serves a bare WebSocket endpoint and hands the server side of
// each connection, wrapped as the adapter under test, to accepted. It returns
// the server URL.
func newHTTPTest(t *testing.T, accepted chan<- *wsSocket) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		sock := newWSSocket(conn)
		accepted <- sock
		<-r.Context().Done() // keep the handler (and the hijacked conn) alive until the test ends
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func closeBody(r *http.Response) {
	if r != nil && r.Body != nil {
		_ = r.Body.Close()
	}
}
