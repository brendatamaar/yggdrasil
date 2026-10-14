package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func newTestServer(t *testing.T, start Starter) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(newServer(start, "", log))
	t.Cleanup(srv.Close)
	return srv
}

func noStart(t *testing.T) Starter {
	return func(Options) (PTY, error) {
		t.Error("the PTY must not start")
		return nil, io.EOF
	}
}

func wsURL(srv *httptest.Server, query string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?" + query
}

func TestRejectsForeignOrigin(t *testing.T) {
	srv := newTestServer(t, noStart(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL(srv, ""), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://evil.example"}},
	})
	if err == nil {
		t.Fatal("dial with a foreign Origin succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %v", resp)
	}
}

func TestRejectsForeignHost(t *testing.T) {
	srv := newTestServer(t, noStart(t))
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestRejectsUnknownShell(t *testing.T) {
	srv := newTestServer(t, noStart(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL(srv, "shell=bash.exe%20-c%20calc"), nil)
	if err == nil {
		t.Fatal("dial with an unknown shell succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %v", resp)
	}
}

func TestServesPage(t *testing.T) {
	srv := newTestServer(t, noStart(t))
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "/assets/xterm/lib/xterm.mjs") {
		t.Fatalf("status %d, body %.80q", resp.StatusCode, body)
	}
}

func TestServesXtermAssets(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"xterm/lib/xterm.mjs", "xterm/css/xterm.css", "xterm/package.json", "addon-fit/lib/addon-fit.mjs"} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(newServer(noStart(t), dir, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		path, wantType string
		wantStatus     int
	}{
		{"/assets/xterm/lib/xterm.mjs", "text/javascript", http.StatusOK},
		{"/assets/addon-fit/lib/addon-fit.mjs", "text/javascript", http.StatusOK},
		{"/assets/xterm/css/xterm.css", "text/css", http.StatusOK},
		{"/assets/xterm/package.json", "", http.StatusNotFound},
	} {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.wantStatus || !strings.HasPrefix(resp.Header.Get("Content-Type"), tc.wantType) {
			t.Errorf("%s: status %d, type %q", tc.path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}
