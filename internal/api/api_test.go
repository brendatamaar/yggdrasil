package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

const secretDetail = "SECRET-DETAIL-42"

type env struct {
	srv     *Server
	ts      *httptest.Server
	store   *db.Store
	project core.Project
	repo    core.Repo
}

// newEnv starts a server on a temp database. Tests that need the task service
// set Deps.Tasks (a fake) in extra.
func newEnv(t *testing.T, extra ...func(*Deps)) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	redactor := safety.NewRedactor()
	store := db.NewStore(d, redactor.Redact)
	deps := Deps{
		Store: store, Redactor: redactor,
		Version: "test",
		Assets: fstest.MapFS{
			"index.html":    {Data: []byte("<html>app</html>")},
			"assets/app.js": {Data: []byte("console.log(1)")},
			"assets/m.mjs":  {Data: []byte("export {}")},
		},
		AllowedOrigins: []string{"http://localhost:5173"},
	}
	for _, f := range extra {
		f(&deps)
	}
	srv, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })

	project := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
	repoDir := filepath.Join(project.Path, "shop-api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := dbtest.Repo(t, store, core.Repo{ProjectID: project.ID, Name: "shop-api", Path: repoDir})
	return &env{srv: srv, ts: ts, store: store, project: project, repo: repo}
}

// reply is what a test needs from a response; do has already closed the body.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (e *env) do(t *testing.T, method, path string, body any, mod ...func(*http.Request)) (reply, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mod {
		m(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, data
}

func (e *env) createReq() CreateTaskRequest {
	return CreateTaskRequest{
		ProjectID: e.project.ID, RepoIDs: []core.RepoID{e.repo.ID},
		// The first line becomes the task title; the rest must never leave the
		// task (not on the tasks topic, not in the audit log).
		Prompt: "Find the order routes in shop-api.\n" + secretDetail, Mode: core.ModeAsk, Agent: "mock",
	}
}

func (e *env) dial(t *testing.T, mod ...func(*websocket.DialOptions)) *websocket.Conn {
	t.Helper()
	opts := &websocket.DialOptions{}
	for _, m := range mod {
		m(opts)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.ts.URL, "http")+"/ws", opts)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func subscribe(t *testing.T, c *websocket.Conn, topic string) {
	t.Helper()
	b, _ := json.Marshal(WSMessage{Type: MsgSubscribe, Topic: topic})
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func readMsg(t *testing.T, c *websocket.Conn, timeout time.Duration) (WSMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		return WSMessage{}, err
	}
	var m WSMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m, nil
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	resp, data := e.do(t, http.MethodGet, "/api/health", nil)
	var h HealthResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &h) != nil || h.Status != "ok" || h.Version != "test" {
		t.Fatalf("health = %d %s", resp.StatusCode, data)
	}
}

func TestHostAndOriginChecks(t *testing.T) {
	e := newEnv(t)
	e.srv.port.Store(int32(e.ts.Listener.Addr().(*net.TCPAddr).Port)) //nolint:gosec // test port
	port := e.ts.Listener.Addr().(*net.TCPAddr).Port
	same := e.ts.URL
	cases := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"loopback ip", "", "", 200},
		{"localhost", "localhost:" + itoa(port), "", 200},
		{"ipv6 loopback", "[::1]:" + itoa(port), "", 200},
		{"dns rebinding", "evil.example", "", 403},
		{"rebinding with port", "evil.example:" + itoa(port), "", 403},
		{"localhost suffix attack", "localhost.evil.com", "", 403},
		{"wrong port", "127.0.0.1:1", "", 403},
		{"same origin", "", same, 200},
		{"foreign origin", "", "https://evil.example", 403},
		{"opaque origin", "", "null", 403},
		{"allowed dev origin", "", "http://localhost:5173", 200},
		{"no origin (curl)", "", "", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, data := e.do(t, http.MethodGet, "/api/health", nil, func(r *http.Request) {
				if c.host != "" {
					r.Host = c.host
				}
				if c.origin != "" {
					r.Header.Set("Origin", c.origin)
				}
			})
			if resp.StatusCode != c.want {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, c.want, data)
			}
			if c.want == 403 {
				var b ErrorBody
				if err := json.Unmarshal(data, &b); err != nil || b.Code != ErrHostRejected || b.Message == "" || b.Hint == "" {
					t.Fatalf("rejection body = %s", data)
				}
			}
		})
	}
}

func TestWebSocketRefusesForeignOrigin(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.ts.URL, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err == nil {
		t.Fatal("websocket accepted a foreign origin")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %v, want 403", resp)
	}
}

func TestErrorFormat(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		method, path string
		body         any
		status       int
		code         ErrorCode
	}{
		{"GET", "/api/tasks/tsk_nope", nil, 404, ErrNotFound},
		{"GET", "/api/nope", nil, 404, ErrNotFound},
		{"GET", "/api/tasks/a%20b", nil, 400, ErrBadRequest},
		{"GET", "/api/tasks?status=bogus", nil, 400, ErrBadRequest},
	} {
		resp, data := e.do(t, c.method, c.path, c.body)
		var b ErrorBody
		if resp.StatusCode != c.status || json.Unmarshal(data, &b) != nil || b.Code != c.code || b.Message == "" {
			t.Errorf("%s %s = %d %s, want %d %s", c.method, c.path, resp.StatusCode, data, c.status, c.code)
		}
	}
}

func TestWebSocketRejectsBadMessages(t *testing.T) {
	e := newEnv(t)
	ws := e.dial(t)
	for _, raw := range []string{`not json`, `{"type":"subscribe","topic":"task:a b"}`, `{"type":"subscribe","topic":"nope"}`, `{"type":"zzz"}`} {
		if err := ws.Write(context.Background(), websocket.MessageText, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		m, err := readMsg(t, ws, 5*time.Second)
		if err != nil || m.Type != MsgError {
			t.Errorf("%s: got %+v, %v; want an error message", raw, m, err)
		}
	}
}

func TestSlowClientIsDroppedWithoutBlocking(t *testing.T) {
	e := newEnv(t)
	ws := e.dial(t)
	subscribe(t, ws, TopicTasks)
	time.Sleep(100 * time.Millisecond)

	big := strings.Repeat("x", 32<<10)
	start := time.Now()
	for range 4000 { // never read by the client: its buffer and the socket fill up
		e.srv.hub.publishNext(tasksTopic{}, map[string]string{"pad": big})
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("publishing took %v: a slow client blocked it", d)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.srv.hub.mu.Lock()
		n := len(e.srv.hub.clients)
		e.srv.hub.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the slow client was never dropped")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSubscriptionsPerClientAreCapped(t *testing.T) {
	e := newEnv(t)
	ws := e.dial(t)
	for i := range maxTopicsPerClient {
		subscribe(t, ws, TopicTaskPrefix+"t"+strconv.Itoa(i))
	}
	subscribe(t, ws, TopicTaskPrefix+"one-too-many")
	m, err := readMsg(t, ws, 5*time.Second)
	if err != nil || m.Type != MsgError || !strings.Contains(string(m.Data), "Too many") {
		t.Fatalf("over the cap: %+v, %v", m, err)
	}
	// A topic the client already has is not "one more".
	subscribe(t, ws, TopicTaskPrefix+"t0")
	unsubscribeAndResubscribe := func() {
		b, _ := json.Marshal(WSMessage{Type: MsgUnsubscribe, Topic: TopicTaskPrefix + "t0"})
		_ = ws.Write(context.Background(), websocket.MessageText, b)
		subscribe(t, ws, TopicTaskPrefix+"fits-now")
	}
	unsubscribeAndResubscribe()
	if m, err := readMsg(t, ws, 300*time.Millisecond); err == nil {
		t.Errorf("unexpected message after making room: %+v", m)
	}
}

func TestContentSecurityPolicy(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.do(t, http.MethodGet, "/", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("CSP allows inline or eval script: %q", csp)
	}
}

func TestStaticAssets(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		path   string
		status int
		ctype  string
		body   string
	}{
		{"/", 200, "text/html", "app"},
		{"/tasks/tsk_1", 200, "text/html", "app"}, // SPA fallback
		{"/assets/app.js", 200, "text/javascript", "console"},
		{"/assets/m.mjs", 200, "text/javascript", "export"},
		{"/missing.js", 404, "application/json", ""},
		{"/CON", 404, "application/json", ""},
		{"/assets/NUL.js", 404, "application/json", ""},
		{"/com1", 404, "application/json", ""},
		{"/assets/..%5c..%5cgo.mod", 404, "", ""},
		{"/c:/windows/win.ini", 404, "application/json", ""},
	} {
		resp, data := e.do(t, http.MethodGet, c.path, nil)
		if resp.StatusCode != c.status {
			t.Errorf("%s = %d, want %d", c.path, resp.StatusCode, c.status)
			continue
		}
		if c.ctype != "" && !strings.HasPrefix(resp.Header.Get("Content-Type"), c.ctype) {
			t.Errorf("%s content type = %q, want %q", c.path, resp.Header.Get("Content-Type"), c.ctype)
		}
		if c.body != "" && !strings.Contains(string(data), c.body) {
			t.Errorf("%s body = %q", c.path, data)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", c.path)
		}
	}
	if resp, _ := e.do(t, http.MethodPost, "/", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", resp.StatusCode)
	}
}

func TestStaticWithoutBuild(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Assets = fstest.MapFS{} })
	resp, data := e.do(t, http.MethodGet, "/", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "task build") {
		t.Errorf("not-built page = %d %s", resp.StatusCode, data)
	}
}

func TestCleanAssetPath(t *testing.T) {
	for _, bad := range []string{"/a/../b", `/a\b`, "/c:/x", "/CON", "/x/con.txt", "/LPT1", "/aux/y", "/nul."} {
		if _, ok := cleanAssetPath(bad); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{"/", "/assets/app.js", "/consul/x", "/com10", "/console.js"} {
		if _, ok := cleanAssetPath(good); !ok {
			t.Errorf("%q was refused", good)
		}
	}
}

type fakeListener struct{ net.Listener }

func (fakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero, Port: 4747} }

func TestRunRefusesNonLoopbackListener(t *testing.T) {
	e := newEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = e.srv.Run(context.Background(), fakeListener{ln})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Run on 0.0.0.0 = %v, want a loopback refusal", err)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
