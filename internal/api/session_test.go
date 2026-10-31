package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// noSession drops the harness cookie, so the request looks like curl or a
// local process that never opened the launch link.
func noSession(r *http.Request) { r.Header.Del("Cookie") }

// noRedirect keeps the redirect of the launch link visible.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// launched is what a test needs from the launch link's response.
type launched struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

func (e *env) launch(t *testing.T, token string) launched {
	t.Helper()
	resp, err := noRedirect.Get(e.ts.URL + "/?k=" + url.QueryEscape(token))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return launched{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}
}

func sessionCookieOf(resp launched) *http.Cookie {
	for _, c := range resp.cookies {
		if c.Name == SessionCookie {
			return c
		}
	}
	return nil
}

func TestAPIRequiresASession(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/api/tasks", "/api/repos", "/api/nope", "/api/mcp/status", "/api/agents"} {
		resp, data := e.do(t, "GET", p, nil, noSession)
		var b ErrorBody
		if resp.StatusCode != 401 || json.Unmarshal(data, &b) != nil || b.Code != ErrSessionRequired || b.Message == "" || b.Hint == "" {
			t.Errorf("GET %s without a session = %d %s", p, resp.StatusCode, data)
		}
	}
	// A made-up cookie is no session.
	resp, _ := e.do(t, "GET", "/api/tasks", nil, noSession, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: strings.Repeat("a", 64)})
	})
	if resp.StatusCode != 401 {
		t.Errorf("forged cookie = %d", resp.StatusCode)
	}
	// And with the harness session the same route works.
	if resp, data := e.do(t, "GET", "/api/tasks", nil); resp.StatusCode != 200 {
		t.Errorf("GET /api/tasks with a session = %d %s", resp.StatusCode, data)
	}
}

func TestWebSocketRequiresASession(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.ts.URL, "http")+"/ws", nil)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err == nil {
		_ = c.CloseNow()
		t.Fatal("the websocket upgraded without a session")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("response = %v, want 401", resp)
	}
	// With the cookie it upgrades.
	e.dial(t)
}

func TestHealthStaticAndMCPStayOpen(t *testing.T) {
	e := newEnv(t)
	for p, want := range map[string]int{
		"/api/health":    200,
		"/":              200,
		"/assets/app.js": 200,
		"/some/route":    200,
	} {
		if resp, data := e.do(t, "GET", p, nil, noSession); resp.StatusCode != want {
			t.Errorf("GET %s without a session = %d %s, want %d", p, resp.StatusCode, data, want)
		}
	}
	// The MCP mount is not gated by the session (it has its own bearer); the
	// stub answers 501 until task M1, which is not a 401.
	if resp, _ := e.do(t, "POST", "/mcp", nil, noSession); resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("POST /mcp wanted the MCP bearer, not the UI session")
	}
}

func TestLaunchLinkSetsTheCookie(t *testing.T) {
	e := newEnv(t)
	tok := e.srv.LaunchToken()
	if len(tok) != 64 {
		t.Fatalf("launch token = %q, want 32 random bytes in hex", tok)
	}

	// Wrong token: no cookie, and the token leaves the address bar anyway.
	for _, bad := range []string{"wrong", "", tok[:63], tok + "0"} {
		resp := e.launch(t, bad)
		if sessionCookieOf(resp) != nil {
			t.Errorf("token %q set a cookie", bad)
		}
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
			t.Errorf("token %q: %d -> %q", bad, resp.StatusCode, resp.Header.Get("Location"))
		}
	}

	resp := e.launch(t, tok)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("launch = %d -> %q, want a redirect to / without the token", resp.StatusCode, resp.Header.Get("Location"))
	}
	c := sessionCookieOf(resp)
	if c == nil {
		t.Fatal("no ygg_session cookie")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || len(c.Value) != 64 || c.Value == tok {
		t.Errorf("cookie = %+v", c)
	}

	// The cookie opens the API; the launch token itself is not a session.
	r, data := e.do(t, "GET", "/api/tasks", nil, noSession, func(r *http.Request) { r.AddCookie(c) })
	if r.StatusCode != 200 {
		t.Errorf("API with the new session = %d %s", r.StatusCode, data)
	}
	r, _ = e.do(t, "GET", "/api/tasks", nil, noSession, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	})
	if r.StatusCode != 401 {
		t.Errorf("the launch token used as a cookie = %d", r.StatusCode)
	}
}

// The M2 case: a local process posting a proposal confirm with no Origin
// header and no cookie must not reach the service.
func TestSelfConfirmWithoutASessionIsRefused(t *testing.T) {
	e, f := proposalsEnv(t)
	resp, _ := e.do(t, http.MethodPost, "/api/proposals/prp_1/confirm", ConfirmProposalRequest{Hash: propHash}, noSession)
	if resp.StatusCode != http.StatusUnauthorized || f.calls != 0 {
		t.Fatalf("confirm without a session = %d, service calls %d", resp.StatusCode, f.calls)
	}
}

func TestInsecureNoSessionIsRefusedWithWriteModes(t *testing.T) {
	_, err := New(Deps{Store: newEnv(t).store, InsecureNoSession: true, WriteModes: true})
	if err == nil {
		t.Fatal("New accepted InsecureNoSession together with WriteModes")
	}
}

func TestInsecureNoSessionOpensTheAPI(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.InsecureNoSession = true })
	if resp, data := e.do(t, "GET", "/api/tasks", nil, noSession); resp.StatusCode != 200 {
		t.Errorf("insecure mode = %d %s", resp.StatusCode, data)
	}
	if e.srv.LaunchToken() != "" {
		t.Error("insecure mode made a launch token")
	}
}

// Invariant 4: the launch token reaches neither the log nor the audit log.
func TestLaunchTokenIsNeverLoggedOrAudited(t *testing.T) {
	var buf bytes.Buffer
	redactor := safety.NewRedactor()
	log := slog.New(safety.NewLogHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}), redactor))
	e := newEnv(t, func(d *Deps) { d.Redactor, d.Log = redactor, log })
	tok := e.srv.LaunchToken()

	e.launch(t, tok)
	e.launch(t, "wrong")
	e.srv.log.Info("careless", "url", "http://127.0.0.1:1/?k="+tok, "token", tok)
	e.srv.log.Error("careless", "err", "bad "+tok)
	if strings.Contains(buf.String(), tok) {
		t.Fatalf("the launch token is in the log:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), safety.Redacted) {
		t.Fatalf("expected the redaction marker in the log:\n%s", buf.String())
	}

	ctx := context.Background()
	entry := core.AuditEntry{ID: "aud_s1", At: time.Now().UTC(), Actor: "user", Action: "x", Subject: "s-1", Detail: json.RawMessage(`{"url":"/?k=` + tok + `"}`)}
	// The production wiring builds the store from the same redactor (cmd/ygg).
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	auditStore := db.NewStore(d, redactor.Redact)
	if err := auditStore.Commit(ctx, func(tx *db.Tx) error { return tx.AppendAudit(ctx, entry) }); err != nil {
		t.Fatal(err)
	}
	rows, err := auditStore.ListAudit(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if strings.Contains(string(r.Detail), tok) || strings.Contains(r.Subject, tok) {
			t.Fatalf("the launch token is in the audit log: %+v", r)
		}
	}
}
