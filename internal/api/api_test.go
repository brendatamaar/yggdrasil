package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
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

// newEnv starts a server on a temp database with the mock adapter. delay
// paces the replay so a test can subscribe or cancel while it runs.
func newEnv(t *testing.T, fixture string, delay time.Duration, extra ...func(*Deps)) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := db.NewStore(d)
	redactor := safety.NewRedactor()
	deps := Deps{
		Store: store, Audit: audit.NewSQLiteWriter(store, redactor), Env: safety.NewEnvBuilder(),
		Adapters: map[string]agents.Adapter{"mock": mock.New(mock.Options{Fixture: fixture, Delay: delay})},
		Version:  "test",
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

	project, err := store.CreateProject(ctx, core.Project{Name: "acme-shop", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.CreateRepo(ctx, core.Repo{ProjectID: project.ID, Name: "shop-api", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
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

func (e *env) create(t *testing.T) core.Task {
	t.Helper()
	resp, data := e.do(t, http.MethodPost, "/api/tasks", e.createReq())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task = %d: %s", resp.StatusCode, data)
	}
	var tr TaskResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		t.Fatal(err)
	}
	return tr.Task
}

func (e *env) waitStatus(t *testing.T, id core.TaskID, want core.TaskStatus) core.Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		task, err := e.store.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == want {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s is %s, want %s", id, task.Status, want)
		}
		time.Sleep(20 * time.Millisecond)
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
	e := newEnv(t, "plain", 0)
	resp, data := e.do(t, http.MethodGet, "/api/health", nil)
	var h HealthResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &h) != nil || h.Status != "ok" || h.Version != "test" {
		t.Fatalf("health = %d %s", resp.StatusCode, data)
	}
}

func TestHostAndOriginChecks(t *testing.T) {
	e := newEnv(t, "plain", 0)
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
	e := newEnv(t, "plain", 0)
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
	e := newEnv(t, "plain", 0)
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
		{"POST", "/api/tasks", map[string]any{"bogusField": 1}, 400, ErrBadRequest},
	} {
		resp, data := e.do(t, c.method, c.path, c.body)
		var b ErrorBody
		if resp.StatusCode != c.status || json.Unmarshal(data, &b) != nil || b.Code != c.code || b.Message == "" {
			t.Errorf("%s %s = %d %s, want %d %s", c.method, c.path, resp.StatusCode, data, c.status, c.code)
		}
	}
}

func TestCreateTaskRefusals(t *testing.T) {
	e := newEnv(t, "plain", 0)
	other, err := e.store.CreateProject(context.Background(), core.Project{Name: "other", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	otherRepo, err := e.store.CreateRepo(context.Background(), core.Repo{ProjectID: other.ID, Name: "x", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	mutate := map[string]func(*CreateTaskRequest){
		"code mode":         func(r *CreateTaskRequest) { r.Mode = core.ModeCode },
		"current branch":    func(r *CreateTaskRequest) { r.Mode = core.ModeCurrentBranch },
		"project mode":      func(r *CreateTaskRequest) { r.Mode = core.ModeProject },
		"empty prompt":      func(r *CreateTaskRequest) { r.Prompt = "  " },
		"unknown agent":     func(r *CreateTaskRequest) { r.Agent = "nope" },
		"two repos":         func(r *CreateTaskRequest) { r.RepoIDs = append(r.RepoIDs, otherRepo.ID) },
		"no repos":          func(r *CreateTaskRequest) { r.RepoIDs = nil },
		"repo of a project": func(r *CreateTaskRequest) { r.RepoIDs = []core.RepoID{otherRepo.ID} },
	}
	for name, m := range mutate {
		req := e.createReq()
		m(&req)
		resp, data := e.do(t, http.MethodPost, "/api/tasks", req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d (%s), want 400", name, resp.StatusCode, data)
		}
	}
	// Nothing was created, and nothing was audited for a refused request.
	tasks, _ := e.store.ListTasks(context.Background(), e.project.ID)
	if len(tasks) != 0 {
		t.Errorf("refused requests created %d tasks", len(tasks))
	}
}

// A repo that is not tracked in the project is never reachable (invariant 6).
func TestCreateTaskUnknownRepoAndProject(t *testing.T) {
	e := newEnv(t, "plain", 0)
	req := e.createReq()
	req.RepoIDs = []core.RepoID{"repo_missing"}
	if resp, _ := e.do(t, http.MethodPost, "/api/tasks", req); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo = %d, want 404", resp.StatusCode)
	}
	req = e.createReq()
	req.ProjectID = "prj_missing"
	if resp, _ := e.do(t, http.MethodPost, "/api/tasks", req); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", resp.StatusCode)
	}
}

func TestTaskRunsAndEventsAreStoredInOrder(t *testing.T) {
	e := newEnv(t, "subagents", 0)
	ws := e.dial(t)
	subscribe(t, ws, TopicTasks)
	time.Sleep(100 * time.Millisecond) // let the subscription register

	task := e.create(t)
	done := e.waitStatus(t, task.ID, core.TaskDone)
	if done.FinishedAt == nil {
		t.Error("finished task has no finish time")
	}

	// The "tasks" topic carried the status changes: no prompt, no output.
	var seen []core.TaskStatus
	for len(seen) < 2 {
		m, err := readMsg(t, ws, 5*time.Second)
		if err != nil {
			t.Fatalf("read tasks topic: %v (seen %v)", err, seen)
		}
		var sm TaskStatusMessage
		if err := json.Unmarshal(m.Data, &sm); err != nil || sm.TaskID != task.ID {
			t.Fatalf("bad tasks message %s", m.Data)
		}
		if strings.Contains(string(m.Data), secretDetail) {
			t.Errorf("tasks topic leaks the prompt: %s", m.Data)
		}
		seen = append(seen, sm.Status)
	}
	if seen[0] != core.TaskRunning || seen[1] != core.TaskDone {
		t.Errorf("status messages = %v, want running then done", seen)
	}

	resp, data := e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/events?after=0", nil)
	var er TaskEventsResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &er) != nil || len(er.Events) < 5 {
		t.Fatalf("events = %d %s", resp.StatusCode, data)
	}
	parents := 0
	for i, ev := range er.Events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d: gap or reorder", i, ev.Seq)
		}
		if ev.ParentID != "" {
			parents++
		}
	}
	if parents == 0 {
		t.Error("nested events lost their ParentID")
	}

	// Backfill after a point returns only the later events.
	_, data = e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/events?after=3", nil)
	var tail TaskEventsResponse
	if err := json.Unmarshal(data, &tail); err != nil || len(tail.Events) != len(er.Events)-3 || tail.Events[0].Seq != 4 {
		t.Errorf("events after 3 = %s", data)
	}
	if resp, _ := e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/events?after=-1", nil); resp.StatusCode != 400 {
		t.Errorf("after=-1 gave %d, want 400", resp.StatusCode)
	}

	// The list endpoint filters by status.
	_, data = e.do(t, http.MethodGet, "/api/tasks?status=done", nil)
	var list TaskListResponse
	if err := json.Unmarshal(data, &list); err != nil || len(list.Tasks) != 1 || list.Tasks[0].ID != task.ID {
		t.Errorf("done list = %s", data)
	}
	_, data = e.do(t, http.MethodGet, "/api/tasks?status=running", nil)
	if err := json.Unmarshal(data, &list); err != nil || len(list.Tasks) != 0 {
		t.Errorf("running list = %s", data)
	}
}

func TestLiveEventsOverWebSocket(t *testing.T) {
	e := newEnv(t, "subagents", 40*time.Millisecond)
	task := e.create(t)
	ws := e.dial(t)
	subscribe(t, ws, TopicTaskPrefix+string(task.ID))

	var last int64
	got := 0
	for {
		m, err := readMsg(t, ws, 5*time.Second)
		if err != nil {
			break
		}
		if m.Type != MsgEvent || m.Topic != TopicTaskPrefix+string(task.ID) {
			t.Fatalf("unexpected message %+v", m)
		}
		var ev agents.Event
		if err := json.Unmarshal(m.Data, &ev); err != nil || ev.Seq != m.Seq {
			t.Fatalf("event data %s (seq %d)", m.Data, m.Seq)
		}
		if last != 0 && m.Seq != last+1 {
			t.Fatalf("seq jumped from %d to %d", last, m.Seq)
		}
		last = m.Seq
		got++
		if ev.Kind == agents.EventStatus && ev.Status == agents.RunCompleted {
			break
		}
	}
	if got < 3 {
		t.Fatalf("only %d live events", got)
	}
	e.waitStatus(t, task.ID, core.TaskDone)
}

func TestWebSocketRejectsBadMessages(t *testing.T) {
	e := newEnv(t, "plain", 0)
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
	e := newEnv(t, "plain", 0)
	ws := e.dial(t)
	subscribe(t, ws, TopicTasks)
	time.Sleep(100 * time.Millisecond)

	big := strings.Repeat("x", 32<<10)
	start := time.Now()
	for range 4000 { // never read by the client: its buffer and the socket fill up
		e.srv.hub.publish(TopicTasks, e.srv.hub.nextSeq(TopicTasks), map[string]string{"pad": big})
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

func TestCancelEndsTheTaskAndIsAudited(t *testing.T) {
	e := newEnv(t, "subagents", 300*time.Millisecond)
	task := e.create(t)
	e.waitStatus(t, task.ID, core.TaskRunning)

	for range 2 { // idempotent
		resp, data := e.do(t, http.MethodPost, "/api/tasks/"+string(task.ID)+"/cancel", nil)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("cancel = %d %s", resp.StatusCode, data)
		}
	}
	e.waitStatus(t, task.ID, core.TaskCancelled)

	rows, err := e.store.ListAudit(context.Background(), "task:"+string(task.ID), 50)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, r := range rows {
		actions[r.Action] = true
		if strings.Contains(string(r.Detail), secretDetail) || strings.Contains(string(r.Detail), "order routes") {
			t.Errorf("audit detail holds the prompt: %s", r.Detail)
		}
	}
	for _, want := range []string{"task.create", "task.start", "task.cancel", "task.finish"} {
		if !actions[want] {
			t.Errorf("no audit entry %q (have %v)", want, actions)
		}
	}
	// The cancelled run still stored its final event.
	_, data := e.do(t, http.MethodGet, "/api/tasks/"+string(task.ID)+"/events", nil)
	var er TaskEventsResponse
	if err := json.Unmarshal(data, &er); err != nil || len(er.Events) == 0 {
		t.Fatalf("events after cancel = %s", data)
	}
	if last := er.Events[len(er.Events)-1]; last.Kind != agents.EventStatus || last.Status != agents.RunCancelled {
		t.Errorf("last event = %+v, want a cancelled status", last)
	}
}

func TestUnavailableAgentIsRefused(t *testing.T) {
	e := newEnv(t, "plain", 0, func(d *Deps) { d.Adapters["broken"] = brokenAdapter{mock.New(mock.Options{})} })
	req := e.createReq()
	req.Agent = "broken"
	resp, data := e.do(t, http.MethodPost, "/api/tasks", req)
	var b ErrorBody
	if resp.StatusCode != 503 || json.Unmarshal(data, &b) != nil || b.Code != ErrUnavailable || !strings.Contains(b.Message, "not signed in") {
		t.Errorf("broken agent = %d %s", resp.StatusCode, data)
	}
}

type brokenAdapter struct{ *mock.Adapter }

func (brokenAdapter) Name() string { return "broken" }
func (brokenAdapter) Check(context.Context) error {
	return errString("The agent is not signed in. Sign in with its own app first.")
}

type errString string

func (e errString) Error() string { return string(e) }

func TestStaticAssets(t *testing.T) {
	e := newEnv(t, "plain", 0)
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
	e := newEnv(t, "plain", 0, func(d *Deps) { d.Assets = fstest.MapFS{} })
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
	e := newEnv(t, "plain", 0)
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

func TestRunServesAndStopsCleanly(t *testing.T) {
	e := newEnv(t, "subagents", 200*time.Millisecond)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.srv.Run(ctx, ln) }()

	base := "http://" + ln.Addr().String()
	var resp *http.Response
	for range 50 {
		if resp, err = http.Get(base + "/api/health"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health on the real listener: %v %v", resp, err)
	}
	resp.Body.Close()

	// Wrong port in the Host header is refused once the port is known.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/health", nil)
	req.Host = "127.0.0.1:1"
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Errorf("wrong Host port: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}

	// A task is running when the server stops: it must be cancelled, and Run
	// must return.
	body, _ := json.Marshal(e.createReq())
	resp, err = http.Post(base+"/api/tasks", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("create on the real listener: %v %v", resp, err)
	}
	resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	tasks, _ := e.store.ListTasks(context.Background(), e.project.ID)
	if len(tasks) != 1 || tasks[0].Status != core.TaskCancelled {
		t.Errorf("after shutdown: %+v, want one cancelled task", tasks)
	}
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Error("New accepted empty deps")
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
