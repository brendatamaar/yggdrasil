package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// appClient sends the UI session of the app a request goes to (the harness
// option: startAppWith opens the session through the real launch link).
var appClient = &http.Client{Transport: &sessionTransport{}}

type sessionTransport struct{ cookies sync.Map } // host:port -> *http.Cookie

func (t *sessionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if c, ok := t.cookies.Load(r.URL.Host); ok {
		r = r.Clone(r.Context())
		r.AddCookie(c.(*http.Cookie)) //nolint:forcetypeassert // only openSession stores here, always a cookie
	}
	return http.DefaultTransport.RoundTrip(r)
}

// openSession trades the launch token for the cookie, as the browser does.
func openSession(t *testing.T, base, token string) {
	t.Helper()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(base + "/?k=" + token)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "ygg_session" {
			appClient.Transport.(*sessionTransport).cookies.Store(strings.TrimPrefix(base, "http://"), c) //nolint:forcetypeassert // set above
			return
		}
	}
	t.Fatalf("the launch link gave no session cookie (status %d)", resp.StatusCode)
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestInsecureNoSessionIsIgnoredWhileWriteModesAreOn(t *testing.T) {
	dir := func() (string, error) { return t.TempDir(), nil }
	cfg, err := loadConfig(envOf(map[string]string{"YGG_INSECURE_NO_SESSION": "1", "YGG_WRITE_MODES": "1"}), dir)
	if err != nil || cfg.noSession || !cfg.noSessionRefused {
		t.Fatalf("both set: %+v, %v", cfg, err)
	}
	cfg, err = loadConfig(envOf(map[string]string{"YGG_INSECURE_NO_SESSION": "1"}), dir)
	if err != nil || !cfg.noSession || cfg.noSessionRefused {
		t.Fatalf("dev only: %+v, %v", cfg, err)
	}
	cfg, err = loadConfig(envOf(nil), dir)
	if err != nil || cfg.noSession || cfg.noSessionRefused {
		t.Fatalf("default: %+v, %v", cfg, err)
	}
	if _, err := loadConfig(envOf(map[string]string{"YGG_INSECURE_NO_SESSION": "maybe"}), dir); err == nil {
		t.Error("YGG_INSECURE_NO_SESSION=maybe was accepted")
	}
}

// The server itself refuses the combination too, whatever config says.
func TestAppRefusesNoSessionWithWriteModes(t *testing.T) {
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir(), writeModes: true, noSession: true}
	_, err := newApp(context.Background(), cfg, fstest.MapFS{"index.html": {Data: []byte("x")}}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("newApp accepted write modes with the UI session off")
	}
}

func TestLaunchMessageCarriesTheTokenOnlyInTheLink(t *testing.T) {
	if got := launchMessage("127.0.0.1:4747", "abc"); !strings.Contains(got, "http://127.0.0.1:4747/?k=abc") {
		t.Errorf("message = %q", got)
	}
	if got := launchMessage("127.0.0.1:4747", ""); strings.Contains(got, "?k=") {
		t.Errorf("session-off message = %q", got)
	}
}

// The real app refuses API calls without the session and accepts them with
// the cookie from the launch link.
func TestAppRequiresTheLaunchSession(t *testing.T) {
	base, _, _ := startApp(t)
	resp, err := http.Get(base + "/api/tasks")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", resp.StatusCode)
	}
	if code := getJSON(t, base+"/api/tasks", nil); code != http.StatusOK {
		t.Fatalf("with the harness session = %d", code)
	}
}
